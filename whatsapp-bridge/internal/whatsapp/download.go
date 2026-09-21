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
	"go.mau.fi/whatsmeow/types/events"
	"whatsapp-bridge/internal/database"
)

var safeComponentRegex = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// defaultMediaRetryTimeout is used when SetMediaRetryTimeout was not called
const defaultMediaRetryTimeout = 30 * time.Second

var errMediaRetryTimeout = errors.New("media retry timed out")

// retryFailureStatus maps a media-retry error to the status recorded in media.db
func retryFailureStatus(err error) string {
	switch {
	case errors.Is(err, errBlobMACMismatch):
		return database.DownloadStatusRekeyedOnPhone
	case errors.Is(err, whatsmeow.ErrMediaNotAvailableOnPhone):
		return database.DownloadStatusNotOnPhone
	case errors.Is(err, errMediaRetryTimeout):
		return database.DownloadStatusRetryTimeout
	default:
		return database.DownloadStatusFailed
	}
}

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

// ChatFolderKey returns the store/media subfolder for a chat from all of its JIDs (as returned
// by ResolveChatJIDs): a group's full JID, else the phone number, else the first JID's user part.
// The batch job and the per-message download both use it, so files and index.json always
// land in the same folder.
func ChatFolderKey(jids []string) string {
	for _, j := range jids {
		if strings.HasSuffix(j, "@g.us") {
			return safePathComponent(j)
		}
	}
	for _, j := range jids {
		if strings.HasSuffix(j, "@s.whatsapp.net") {
			return safePathComponent(strings.SplitN(j, "@", 2)[0])
		}
	}
	if len(jids) > 0 {
		return safePathComponent(strings.SplitN(jids[0], "@", 2)[0])
	}
	return "unknown"
}

// mediaFolderKey resolves a chat JID's folder key once and caches it, so a batch doesn't
// open whatsapp.db for every message
func (c *Client) mediaFolderKey(chatJID, storeDir string) string {
	if key, ok := c.folderKeys.Load(chatJID); ok {
		return key.(string)
	}
	jids, err := ResolveChatJIDs(chatJID, storeDir)
	if err != nil || len(jids) == 0 {
		jids = []string{chatJID}
	}
	key := ChatFolderKey(jids)
	c.folderKeys.Store(chatJID, key)
	return key
}

// DownloadOptions configures media download behavior
type DownloadOptions struct {
	SkipMediaRetry bool
}

// resolveDirectPath picks the CDN path to download from.
//
// Schema v1 captures direct_path straight from the protobuf, query string included
// (ccb/oh/oe/_nc_sid — the auth token whose loss was bug S2), so it needs no parsing and
// is present even when url is not. Rows stored before v1 have no direct_path and still
// have it derived from their url.
func resolveDirectPath(info *database.MediaInfo) string {
	if info == nil {
		return ""
	}
	if info.DirectPath != "" {
		return info.DirectPath
	}
	return ExtractDirectPathFromURL(info.URL)
}

// DownloadMedia downloads an audio message, checking cache, downloading from WhatsApp,
// and triggering media-retry receipts if CDN tokens are expired.
func (c *Client) DownloadMedia(
	ctx context.Context,
	mediaStore *database.MediaStore,
	messageStore *database.MessageStore,
	messageID, chatJID, storeDir string,
) (*DownloadResult, error) {
	return c.DownloadMediaWithOptions(ctx, mediaStore, messageStore, messageID, chatJID, storeDir, DownloadOptions{})
}

// DownloadMediaWithOptions downloads an audio message with additional options (e.g. SkipMediaRetry).
func (c *Client) DownloadMediaWithOptions(
	ctx context.Context,
	mediaStore *database.MediaStore,
	messageStore *database.MessageStore,
	messageID, chatJID, storeDir string,
	opts DownloadOptions,
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

	folderKey := c.mediaFolderKey(chatJID, storeDir)
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
	if err == nil && rec != nil && rec.Status == database.DownloadStatusOK && rec.LocalPath != "" {
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
			Status:    database.DownloadStatusIncompleteMetadata,
			Error:     "missing decryption key or hashes in database",
		})
		return nil, fmt.Errorf("incomplete decryption material for message %s", messageID)
	}

	directPath := resolveDirectPath(info)
	if directPath == "" {
		_ = mediaStore.RecordDownload(&database.MediaDownloadRecord{
			MessageID: messageID,
			ChatJID:   chatJID,
			Status:    database.DownloadStatusIncompleteMetadata,
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
			if opts.SkipMediaRetry {
				_ = mediaStore.RecordDownload(&database.MediaDownloadRecord{
					MessageID:  messageID,
					ChatJID:    chatJID,
					DirectPath: directPath,
					Status:     database.DownloadStatusFailed,
					Error:      "direct download expired: " + err.Error(),
				})
				return nil, fmt.Errorf("direct download expired (media retry skipped): %w", err)
			}
			c.logger.Infof("Direct download expired (403/404/410) for message %s, initiating media retry...", messageID)
			var retryPath string
			data, retryPath, err = c.downloadWithMediaRetry(ctx, messageID, chatJID, info, whatsmeow.MediaAudio)
			if err != nil {
				status := retryFailureStatus(err)
				// Keep the fresh path from the retry (if any) so a failure can be re-examined
				recordedPath := directPath
				if retryPath != "" {
					recordedPath = retryPath
				}
				_ = mediaStore.RecordDownload(&database.MediaDownloadRecord{
					MessageID:  messageID,
					ChatJID:    chatJID,
					DirectPath: recordedPath,
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
				Status:     database.DownloadStatusFailed,
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
			Status:     database.DownloadStatusFailed,
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
		Status:     database.DownloadStatusOK,
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

	c.logger.Infof("Requesting media retry for %s (chat=%s, sender=%s, is_from_me=%v)",
		messageID, chat, sender, info.IsFromMe)
	if err := c.Client.SendMediaRetryReceipt(ctx, msgInfo, info.MediaKey); err != nil {
		return nil, "", fmt.Errorf("failed to send media retry receipt: %w", err)
	}

	timeout := c.MediaRetryTimeout()
	var evt *events.MediaRetry
	select {
	case evt = <-waiterCh:
	case <-time.After(timeout):
		return nil, "", fmt.Errorf("%w after %v (phone offline?)", errMediaRetryTimeout, timeout)
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}

	// ErrMediaNotAvailableOnPhone comes back from here when the phone no longer has the file
	notif, err := whatsmeow.DecryptMediaRetryNotification(evt, info.MediaKey)
	if err != nil {
		return nil, "", fmt.Errorf("media retry answer: %w", err)
	}
	switch notif.GetResult() {
	case waMmsRetry.MediaRetryNotification_SUCCESS:
	case waMmsRetry.MediaRetryNotification_NOT_FOUND:
		return nil, "", fmt.Errorf("media retry answer NOT_FOUND: %w", whatsmeow.ErrMediaNotAvailableOnPhone)
	default:
		return nil, "", fmt.Errorf("media retry answer: %v", notif.GetResult())
	}

	freshPath := notif.GetDirectPath()
	c.logger.Infof("Media retry for %s succeeded; fresh direct path received", messageID)

	data, err := c.Client.DownloadMediaWithPath(ctx, freshPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, waType, "", false)
	if err == nil {
		return data, freshPath, nil
	}
	if !errors.Is(err, whatsmeow.ErrInvalidMediaEncSHA256) {
		return nil, freshPath, fmt.Errorf("download using refreshed direct path failed: %w", err)
	}

	// The re-upload's ciphertext differs from the original. Fetch it without the ciphertext
	// hash check and verify it with the original media key instead: the MAC and the
	// plaintext SHA256 must both match. In practice (see AUDIO_DOWNLOAD_PLAN.md §2.5) the
	// phone re-encrypts with a new key it doesn't send back, so this ends in errBlobMACMismatch.
	blob, err := fetchRawMedia(ctx, freshPath)
	if err != nil {
		return nil, freshPath, fmt.Errorf("raw fetch of refreshed direct path failed: %w", err)
	}
	data, err = decryptMediaBlob(blob, info.MediaKey, info.FileSHA256, waType)
	if err != nil {
		return nil, freshPath, fmt.Errorf("re-uploaded media rejected: %w", err)
	}
	return data, freshPath, nil
}
