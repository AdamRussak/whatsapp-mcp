package whatsapp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"whatsapp-bridge/internal/database"
)

var safeComponentRegex = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// safePathComponent sanitizes a string to prevent directory traversal or invalid path characters
func safePathComponent(s string) string {
	return safeComponentRegex.ReplaceAllString(s, "_")
}

// DownloadResult represents the outcome of a media download
type DownloadResult struct {
	Path      string `json:"path"`       // relative to storeDir, e.g. media/972527872233/...ogg
	AbsPath   string `json:"abs_path"`   // absolute file path on disk
	Filename  string `json:"filename"`   // filename on disk
	MediaType string `json:"media_type"` // "audio"
	Mimetype  string `json:"mimetype"`   // "audio/ogg; codecs=opus"
	Cached    bool   `json:"cached"`     // whether file was already cached on disk
	Size      int    `json:"size"`       // size in bytes
	SHA256    string `json:"sha256"`     // hex sha256
}

// isExpiredMediaErr checks if WhatsApp returned 403, 404, or 410 indicating expired CDN tokens
func isExpiredMediaErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410)
}

// writeAtomic writes data to a temporary file in the same directory, flushes, fsyncs, and renames
func writeAtomic(targetPath string, data []byte) error {
	tmpPath := targetPath + ".part"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, targetPath)
}

// resolveTargetFolder returns the subfolder key inside store/media for a given chat JID
func resolveTargetFolder(chatJID, storeDir string) string {
	if strings.HasSuffix(chatJID, "@g.us") {
		return safePathComponent(chatJID)
	}

	jids, err := ResolveChatJIDs(chatJID, storeDir)
	if err == nil {
		for _, j := range jids {
			if strings.HasSuffix(j, "@s.whatsapp.net") {
				user := strings.SplitN(j, "@", 2)[0]
				return safePathComponent(user)
			}
		}
	}

	user := strings.SplitN(chatJID, "@", 2)[0]
	return safePathComponent(user)
}

// DownloadMedia downloads an audio message, checking cache, downloading from WhatsApp,
// and triggering media-retry receipts if CDN tokens are expired.
func (c *Client) DownloadMedia(
	ctx context.Context,
	mediaStore *database.MediaStore,
	messageStore *database.MessageStore,
	messageID, chatJID, storeDir string,
) (*DownloadResult, error) {
	if storeDir == "" {
		storeDir = "store"
	}

	info, err := messageStore.GetMediaInfo(messageID, chatJID)
	if err != nil {
		return nil, fmt.Errorf("failed to query message info: %w", err)
	}
	if info == nil {
		return nil, fmt.Errorf("message %s not found in chat %s", messageID, chatJID)
	}
	if info.MediaType != "audio" {
		return nil, fmt.Errorf("message %s is not an audio message (type: %s)", messageID, info.MediaType)
	}

	folderKey := resolveTargetFolder(chatJID, storeDir)
	targetDir := filepath.Join(storeDir, "media", folderKey)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create media directory: %w", err)
	}

	// Format filename: YYYY-MM-DD_HHMMSS_<me|them>_<msgID>.ogg
	who := "them"
	if info.IsFromMe {
		who = "me"
	}
	tsStr := info.Timestamp.UTC().Format("2006-01-02_150405")
	filename := fmt.Sprintf("%s_%s_%s.ogg", tsStr, who, safePathComponent(info.ID))
	targetPath := filepath.Join(targetDir, filename)
	absPath, _ := filepath.Abs(targetPath)
	relPath, _ := filepath.Rel(storeDir, targetPath)

	// 1. Check local disk & mediaStore cache
	if fi, err := os.Stat(targetPath); err == nil && fi.Size() > 0 {
		rec, _ := mediaStore.GetDownload(messageID, chatJID)
		shaStr := ""
		if rec != nil {
			shaStr = rec.SHA256
		}
		return &DownloadResult{
			Path:      relPath,
			AbsPath:   absPath,
			Filename:  filename,
			MediaType: "audio",
			Mimetype:  "audio/ogg; codecs=opus",
			Cached:    true,
			Size:      int(fi.Size()),
			SHA256:    shaStr,
		}, nil
	}

	// Also check if media.db already has a record with a valid local path
	rec, err := mediaStore.GetDownload(messageID, chatJID)
	if err == nil && rec != nil && rec.Status == "ok" && rec.LocalPath != "" {
		cachedFilePath := filepath.Join(storeDir, rec.LocalPath)
		if fi, err := os.Stat(cachedFilePath); err == nil && fi.Size() > 0 {
			cachedAbs, _ := filepath.Abs(cachedFilePath)
			return &DownloadResult{
				Path:      rec.LocalPath,
				AbsPath:   cachedAbs,
				Filename:  filepath.Base(rec.LocalPath),
				MediaType: "audio",
				Mimetype:  "audio/ogg; codecs=opus",
				Cached:    true,
				Size:      int(fi.Size()),
				SHA256:    rec.SHA256,
			}, nil
		}
	}

	// 2. Validate decryption material
	if len(info.MediaKey) != 32 || len(info.FileSHA256) == 0 || len(info.FileEncSHA256) == 0 {
		_ = mediaStore.RecordDownload(&database.MediaDownloadRecord{
			MessageID: messageID,
			ChatJID:   chatJID,
			Status:    "incomplete_metadata",
			Error:     "missing decryption key or hashes in database",
		})
		return nil, fmt.Errorf("incomplete decryption material for message %s", messageID)
	}

	directPath := ExtractDirectPathFromURL(info.URL)
	if directPath == "" {
		_ = mediaStore.RecordDownload(&database.MediaDownloadRecord{
			MessageID: messageID,
			ChatJID:   chatJID,
			Status:    "incomplete_metadata",
			Error:     "missing direct_path / URL in database",
		})
		return nil, fmt.Errorf("no URL or direct_path available for message %s", messageID)
	}

	// 3. Attempt direct download via WhatsApp MMS CDN
	var data []byte
	var finalDirectPath = directPath

	data, err = c.Client.DownloadMediaWithPath(ctx, directPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, whatsmeow.MediaAudio, "", false)
	if err != nil {
		if isExpiredMediaErr(err) {
			c.logger.Infof("Direct download expired (403/404/410) for message %s, initiating media retry...", messageID)
			var retryPath string
			data, retryPath, err = c.downloadWithMediaRetry(ctx, messageID, chatJID, info, whatsmeow.MediaAudio)
			if err != nil {
				status := "failed"
				if errors.Is(err, whatsmeow.ErrMediaNotAvailableOnPhone) {
					status = "expired_not_on_phone"
				} else if strings.Contains(err.Error(), "timed out") {
					status = "retry_timeout"
				}
				_ = mediaStore.RecordDownload(&database.MediaDownloadRecord{
					MessageID:  messageID,
					ChatJID:    chatJID,
					DirectPath: directPath,
					Status:     status,
					Error:      err.Error(),
				})
				return nil, fmt.Errorf("media download & retry failed: %w", err)
			}
			finalDirectPath = retryPath
		} else {
			_ = mediaStore.RecordDownload(&database.MediaDownloadRecord{
				MessageID:  messageID,
				ChatJID:    chatJID,
				DirectPath: directPath,
				Status:     "failed",
				Error:      err.Error(),
			})
			return nil, fmt.Errorf("direct media download failed: %w", err)
		}
	}

	// 4. Verify downloaded plaintext hash
	plainHash := sha256.Sum256(data)
	if len(info.FileSHA256) == 32 && plainHash != [32]byte(info.FileSHA256) {
		err := fmt.Errorf("decrypted file SHA256 mismatch for message %s", messageID)
		_ = mediaStore.RecordDownload(&database.MediaDownloadRecord{
			MessageID:  messageID,
			ChatJID:    chatJID,
			DirectPath: finalDirectPath,
			Status:     "failed",
			Error:      err.Error(),
		})
		return nil, err
	}

	// 5. Atomic write to destination file
	if err := writeAtomic(targetPath, data); err != nil {
		return nil, fmt.Errorf("failed to save decrypted audio to %s: %w", targetPath, err)
	}

	shaHex := fmt.Sprintf("%x", plainHash[:])
	_ = mediaStore.RecordDownload(&database.MediaDownloadRecord{
		MessageID:  messageID,
		ChatJID:    chatJID,
		LocalPath:  relPath,
		SHA256:     shaHex,
		DirectPath: finalDirectPath,
		Status:     "ok",
		Error:      "",
	})

	return &DownloadResult{
		Path:      relPath,
		AbsPath:   absPath,
		Filename:  filename,
		MediaType: "audio",
		Mimetype:  "audio/ogg; codecs=opus",
		Cached:    false,
		Size:      len(data),
		SHA256:    shaHex,
	}, nil
}

// downloadWithMediaRetry requests the sender's phone to re-upload media and awaits the response
func (c *Client) downloadWithMediaRetry(
	ctx context.Context,
	messageID, chatJID string,
	info *database.MediaInfo,
	waType whatsmeow.MediaType,
) ([]byte, string, error) {
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return nil, "", fmt.Errorf("invalid chat JID %q: %w", chatJID, err)
	}

	var sender types.JID
	if info.IsFromMe {
		if c.Client.Store.ID == nil {
			return nil, "", fmt.Errorf("client not logged in (own device ID is nil)")
		}
		sender = c.Client.Store.ID.ToNonAD()
	} else if info.Sender != "" {
		sender, err = types.ParseJID(info.Sender)
		if err != nil {
			if !strings.Contains(info.Sender, "@") {
				sender, _ = types.ParseJID(info.Sender + "@s.whatsapp.net")
			}
		}
	} else {
		sender = chat
	}

	msgInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   sender,
			IsFromMe: info.IsFromMe,
			IsGroup:  chat.Server == "g.us",
		},
		ID: types.MessageID(messageID),
	}

	waiterCh := c.RegisterMediaRetryWaiter(messageID)
	defer c.UnregisterMediaRetryWaiter(messageID)

	placeholderCh := c.RegisterPlaceholderWaiter(messageID)
	defer c.UnregisterPlaceholderWaiter(messageID)

	c.logger.Infof("Initiating recovery for %s (chat=%s, sender=%s, is_from_me=%v)...",
		messageID, chat, sender, info.IsFromMe)

	// Send placeholder message resend request to primary phone (peer message)
	peerReq := c.Client.BuildUnavailableMessageRequest(chat, sender, messageID)
	sendResp, peerErr := c.Client.SendPeerMessage(ctx, peerReq)
	c.logger.Infof("Sent BuildUnavailableMessageRequest for %s (peerErr: %v, sendResp: %+v)", messageID, peerErr, sendResp)

	// Send media retry receipt (server-error receipt)
	retryErr := c.Client.SendMediaRetryReceipt(ctx, msgInfo, info.MediaKey)
	c.logger.Infof("Sent SendMediaRetryReceipt for %s (retryErr: %v)", messageID, retryErr)

	timeout := 30 * time.Second
	deadline := time.After(timeout)

	var lastDirectPath string
	for {
		select {
		case placeholderEvt := <-placeholderCh:
			c.logger.Infof("Placeholder message resend received for %s! EvtID: %s, UnavailableReqID: %s",
				messageID, placeholderEvt.Info.ID, placeholderEvt.UnavailableRequestID)
			if placeholderEvt.Message != nil {
				if aud := placeholderEvt.Message.GetAudioMessage(); aud != nil {
					c.logger.Infof("Placeholder contains AudioMessage! DirectPath: %s, MediaKey: %x, FileEncSHA: %x, FileSHA: %x",
						aud.GetDirectPath(), aud.GetMediaKey(), aud.GetFileEncSHA256(), aud.GetFileSHA256())
					data, err := c.Client.Download(ctx, aud)
					if err == nil && len(data) > 0 {
						return data, aud.GetDirectPath(), nil
					}
					c.logger.Warnf("Download using placeholder AudioMessage failed: %v", err)
				}
			}

		case evt := <-waiterCh:
			notif, err := whatsmeow.DecryptMediaRetryNotification(evt, info.MediaKey)
			if err != nil {
				c.logger.Warnf("Failed to decrypt media retry notification for %s: %v", messageID, err)
				continue
			}
			if notif.GetResult() != waMmsRetry.MediaRetryNotification_SUCCESS {
				return nil, "", fmt.Errorf("media retry result was not successful: %v", notif.GetResult())
			}

			lastDirectPath = notif.GetDirectPath()
			msgSec := notif.GetMessageSecret()
			c.logger.Infof("Media retry notification SUCCESS for %s! Fresh direct path: %s, StanzaID: %s, MsgSec len: %d",
				messageID, lastDirectPath, notif.GetStanzaID(), len(msgSec))

			// Try downloading with whatsmeow DownloadMediaWithPath
			data, err := c.Client.DownloadMediaWithPath(ctx, lastDirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, waType, "", false)
			if err == nil && len(data) > 0 {
				return data, lastDirectPath, nil
			}
			c.logger.Warnf("Download using media retry direct path with original FileEncSHA256 failed: %v", err)

		case <-deadline:
			return nil, lastDirectPath, fmt.Errorf("media recovery timed out after %v", timeout)

		case <-ctx.Done():
			return nil, lastDirectPath, ctx.Err()
		}
	}
}

