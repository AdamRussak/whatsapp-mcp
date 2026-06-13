package whatsapp

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/types"

	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// GetChatName determines the appropriate name for a chat based on JID and other info
func (c *Client) GetChatName(messageStore *database.MessageStore, jid waTypes.JID, chatJID string, conversation interface{}, sender string) string {
	// First, check if chat already exists in database with a name
	var existingName string
	err := messageStore.GetDB().QueryRow("SELECT name FROM chats WHERE jid = ?", chatJID).Scan(&existingName)
	if err == nil && existingName != "" {
		// Chat exists with a name, use that
		c.logger.Infof("Using existing chat name for %s: %s", chatJID, existingName)
		return existingName
	}

	// Need to determine chat name
	var name string

	if jid.Server == "g.us" {
		// This is a group chat
		c.logger.Infof("Getting name for group: %s", chatJID)

		// Use conversation data if provided (from history sync)
		if conversation != nil {
			// Extract name from conversation if available
			// This uses type assertions to handle different possible types
			var displayName, convName *string
			// Try to extract the fields we care about regardless of the exact type
			v := reflect.ValueOf(conversation)
			if v.Kind() == reflect.Ptr && !v.IsNil() {
				v = v.Elem()

				// Try to find DisplayName field
				if displayNameField := v.FieldByName("DisplayName"); displayNameField.IsValid() && displayNameField.Kind() == reflect.Ptr && !displayNameField.IsNil() {
					dn := displayNameField.Elem().String()
					displayName = &dn
				}

				// Try to find Name field
				if nameField := v.FieldByName("Name"); nameField.IsValid() && nameField.Kind() == reflect.Ptr && !nameField.IsNil() {
					n := nameField.Elem().String()
					convName = &n
				}
			}

			// Use the name we found
			if displayName != nil && *displayName != "" {
				name = *displayName
			} else if convName != nil && *convName != "" {
				name = *convName
			}
		}

		// If we didn't get a name, try group info
		if name == "" {
			groupInfo, err := c.GetGroupInfo(context.Background(), jid)
			if err == nil && groupInfo.Name != "" {
				name = groupInfo.Name
			} else {
				// Fallback name for groups
				name = fmt.Sprintf("Group %s", jid.User)
			}
		}

		c.logger.Infof("Using group name: %s", name)
	} else {
		// This is an individual contact
		c.logger.Infof("Getting name for contact: %s", chatJID)

		// Just use contact info (full name)
		contact, err := c.Store.Contacts.GetContact(context.Background(), jid)
		if err == nil && contact.FullName != "" {
			name = contact.FullName
		} else if sender != "" {
			// Fallback to sender
			name = sender
		} else {
			// Last fallback to JID
			name = jid.User
		}

		c.logger.Infof("Using contact name: %s", name)
	}

	return name
}

// HandleMessage processes regular incoming messages with media support and webhook processing
func (c *Client) HandleMessage(messageStore *database.MessageStore, webhookManager interface{}, msg *events.Message) {
	// Save message to database
	chatJID := msg.Info.Chat.String()
	sender := msg.Info.Sender.User

	// Get appropriate chat name (pass nil for conversation since we don't have one for regular messages)
	name := c.GetChatName(messageStore, msg.Info.Chat, chatJID, nil, sender)

	// Update chat in database with the message timestamp (keeps last message time updated)
	err := messageStore.StoreChat(chatJID, name, msg.Info.Timestamp)
	if err != nil {
		c.logger.Warnf("Failed to store chat: %v", err)
	}

	// Extract text content
	content := ExtractTextContent(msg.Message)

	// Extract media info
	mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength := ExtractMediaInfo(msg.Message)

	// Handle poll vote messages - attempt to decrypt the vote
	var pollVoteInfo *types.PollVoteInfo
	if mediaType == "poll_vote" {
		pollUpdate := msg.Message.GetPollUpdateMessage()
		if pollUpdate != nil {
			creationMsgKey := pollUpdate.GetPollCreationMessageKey()
			creationMsgID := ""
			if creationMsgKey != nil {
				creationMsgID = creationMsgKey.GetID()
			}

			pollVoteInfo = &types.PollVoteInfo{
				Voter:             sender,
				CreationMessageID: creationMsgID,
				SelectedOptions:   []string{},
				SelectedHashes:    []string{},
			}

			// Attempt to decrypt the poll vote
			decryptedVote, decryptErr := c.DecryptPollVote(context.Background(), msg)
			if decryptErr != nil {
				c.logger.Warnf("Failed to decrypt poll vote: %v", decryptErr)
			} else if decryptedVote != nil {
				for _, hash := range decryptedVote.GetSelectedOptions() {
					hashHex := fmt.Sprintf("%x", hash)
					pollVoteInfo.SelectedHashes = append(pollVoteInfo.SelectedHashes, hashHex)
					
					// Look up option name from database
					optName, err := messageStore.GetPollOptionName(creationMsgID, hashHex)
					if err == nil && optName != "" {
						pollVoteInfo.SelectedOptions = append(pollVoteInfo.SelectedOptions, optName)
					} else {
						// Use hash prefix as fallback if name not found
						pollVoteInfo.SelectedOptions = append(pollVoteInfo.SelectedOptions, "Unknown Option ("+hashHex[:8]+")")
					}
				}
			}

			content = fmt.Sprintf("Poll vote from %s (poll: %s)", sender, creationMsgID)
			c.logger.Infof("Poll vote from %s on poll %s: %d option(s) selected", sender, creationMsgID, len(pollVoteInfo.SelectedHashes))
		}
	}

	// Handle poll creation messages
	var pollInfo *types.PollInfo
	if mediaType == "poll" {
		pollInfo = ExtractPollInfo(msg.Message)
		if pollInfo != nil {
			optionNames := make([]string, len(pollInfo.Options))
			for i, opt := range pollInfo.Options {
				optionNames[i] = opt.Name
			}
			c.logger.Infof("Poll created: %q with options: %v (select up to %d)", pollInfo.Question, optionNames, pollInfo.SelectableCount)

			// Store poll options for later mapping when votes arrive
			err := messageStore.StorePollOptions(msg.Info.ID, pollInfo.Options)
			if err != nil {
				c.logger.Warnf("Failed to store poll options: %v", err)
			}
		}
	}

	// Skip if there's no content and no media
	if content == "" && mediaType == "" {
		return
	}

	// Store message in database
	err = messageStore.StoreMessage(
		msg.Info.ID,
		chatJID,
		sender,
		content,
		msg.Info.Timestamp,
		msg.Info.IsFromMe,
		mediaType,
		filename,
		url,
		mediaKey,
		fileSHA256,
		fileEncSHA256,
		fileLength,
	)

	if err != nil {
		c.logger.Warnf("Failed to store message: %v", err)
	} else {
		// Log message reception
		timestamp := msg.Info.Timestamp.Format("2006-01-02 15:04:05")
		direction := "←"
		if msg.Info.IsFromMe {
			direction = "→"
		}

		// Log based on message type
		if mediaType == "poll" {
			fmt.Printf("[%s] %s %s: [POLL] %s\n", timestamp, direction, sender, content)
		} else if mediaType == "poll_vote" {
			fmt.Printf("[%s] %s %s: [POLL VOTE] %s\n", timestamp, direction, sender, content)
		} else if mediaType != "" {
			fmt.Printf("[%s] %s %s: [%s: %s] %s\n", timestamp, direction, sender, mediaType, filename, content)
		} else if content != "" {
			fmt.Printf("[%s] %s %s: %s\n", timestamp, direction, sender, content)
		}
	}

	// Process webhooks if manager is available
	if webhookManager != nil {
		// Cast to webhook manager and process message
		if wm, ok := webhookManager.(interface {
			ProcessMessage(client interface{}, msg *events.Message, chatName string, pollInfo *types.PollInfo, pollVoteInfo *types.PollVoteInfo)
		}); ok {
			wm.ProcessMessage(c, msg, name, pollInfo, pollVoteInfo)
		}
	}
}

// HandleHistorySync processes history sync events
func (c *Client) HandleHistorySync(messageStore *database.MessageStore, historySync *events.HistorySync) {
	fmt.Printf("Received history sync event with %d conversations\n", len(historySync.Data.Conversations))

	syncedCount := 0
	for _, conversation := range historySync.Data.Conversations {
		// Parse JID from the conversation
		if conversation.ID == nil {
			continue
		}

		chatJID := *conversation.ID

		// Try to parse the JID
		jid, err := waTypes.ParseJID(chatJID)
		if err != nil {
			c.logger.Warnf("Failed to parse JID %s: %v", chatJID, err)
			continue
		}

		// Get appropriate chat name by passing the history sync conversation directly
		name := c.GetChatName(messageStore, jid, chatJID, conversation, "")

		// Process messages
		messages := conversation.Messages
		if len(messages) > 0 {
			// Update chat with latest message timestamp
			latestMsg := messages[0]
			if latestMsg == nil || latestMsg.Message == nil {
				continue
			}

			// Get timestamp from message info
			timestamp := time.Time{}
			if ts := latestMsg.Message.GetMessageTimestamp(); ts != 0 {
				timestamp = time.Unix(int64(ts), 0)
			} else {
				continue
			}

			messageStore.StoreChat(chatJID, name, timestamp)

			// Store messages
			for _, msg := range messages {
				if msg == nil || msg.Message == nil {
					continue
				}

				// Extract text content
				var content string
				if msg.Message.Message != nil {
					if conv := msg.Message.Message.GetConversation(); conv != "" {
						content = conv
					} else if ext := msg.Message.Message.GetExtendedTextMessage(); ext != nil {
						content = ext.GetText()
					}
				}

				// Extract media info
				var mediaType, filename, url string
				var mediaKey, fileSHA256, fileEncSHA256 []byte
				var fileLength uint64

				if msg.Message.Message != nil {
					mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength = ExtractMediaInfo(msg.Message.Message)
				}

				// Log the message content for debugging
				c.logger.Infof("Message content: %v, Media Type: %v", content, mediaType)

				// Skip messages with no content and no media
				if content == "" && mediaType == "" {
					continue
				}

				// Determine sender
				var sender string
				isFromMe := false
				if msg.Message.Key != nil {
					if msg.Message.Key.FromMe != nil {
						isFromMe = *msg.Message.Key.FromMe
					}
					if !isFromMe && msg.Message.Key.Participant != nil && *msg.Message.Key.Participant != "" {
						sender = *msg.Message.Key.Participant
					} else if isFromMe {
						sender = c.Store.ID.User
					} else {
						sender = jid.User
					}
				} else {
					sender = jid.User
				}

				// Store message
				msgID := ""
				if msg.Message.Key != nil && msg.Message.Key.ID != nil {
					msgID = *msg.Message.Key.ID
				}

				// Get message timestamp
				timestamp := time.Time{}
				if ts := msg.Message.GetMessageTimestamp(); ts != 0 {
					timestamp = time.Unix(int64(ts), 0)
				} else {
					continue
				}

				err = messageStore.StoreMessage(
					msgID,
					chatJID,
					sender,
					content,
					timestamp,
					isFromMe,
					mediaType,
					filename,
					url,
					mediaKey,
					fileSHA256,
					fileEncSHA256,
					fileLength,
				)
				if err != nil {
					c.logger.Warnf("Failed to store history message: %v", err)
				} else {
					syncedCount++
					// Log successful message storage
					if mediaType != "" {
						c.logger.Infof("Stored message: [%s] %s -> %s: [%s: %s] %s",
							timestamp.Format("2006-01-02 15:04:05"), sender, chatJID, mediaType, filename, content)
					} else {
						c.logger.Infof("Stored message: [%s] %s -> %s: %s",
							timestamp.Format("2006-01-02 15:04:05"), sender, chatJID, content)
					}

					// Store poll options for history sync if it's a poll
					if mediaType == "poll" && msg.Message.Message != nil {
						pollInfo := ExtractPollInfo(msg.Message.Message)
						if pollInfo != nil {
							err := messageStore.StorePollOptions(msgID, pollInfo.Options)
							if err != nil {
								c.logger.Warnf("Failed to store poll options from history sync: %v", err)
							}
						}
					}
				}
			}
		}
	}

	fmt.Printf("History sync complete. Stored %d messages.\n", syncedCount)
}
