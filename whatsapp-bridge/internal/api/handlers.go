package api

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/types"
	"whatsapp-bridge/internal/whatsapp"
)

// handleSendMessage handles the message sending API endpoint
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	// Only allow POST requests
	if r.Method != http.MethodPost {
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse the request body
	var req types.SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		SendJSONError(w, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Validate request
	if req.Recipient == "" {
		SendJSONError(w, "Recipient is required", http.StatusBadRequest)
		return
	}

	if req.Message == "" && req.MediaPath == "" {
		SendJSONError(w, "Message or media path is required", http.StatusBadRequest)
		return
	}

	fmt.Println("Received request to send message", req.Message, req.MediaPath)

	// Send the message
	success, message := s.client.SendMessage(s.messageStore, req.Recipient, req.Message, req.MediaPath)
	fmt.Println("Message sent", success, message)

	// Set response headers
	w.Header().Set("Content-Type", "application/json")

	// Set appropriate status code
	if !success {
		w.WriteHeader(http.StatusInternalServerError)
	}

	// Send response
	json.NewEncoder(w).Encode(types.SendMessageResponse{
		Success: success,
		Message: message,
	})
}

// handleWebhooks handles webhook CRUD operations
func (s *Server) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		// List all webhook configurations
		configs := s.webhookManager.GetWebhookConfigs()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    configs,
		})

	case http.MethodPost:
		// Create new webhook configuration
		var config types.WebhookConfig
		if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
			SendJSONError(w, "Invalid request format", http.StatusBadRequest)
			return
		}

		// Validate configuration
		if err := s.webhookManager.ValidateWebhookConfig(&config); err != nil {
			SendJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Store configuration
		if err := s.messageStore.StoreWebhookConfig(&config); err != nil {
			SendJSONError(w, fmt.Sprintf("Failed to store webhook config: %v", err), http.StatusInternalServerError)
			return
		}

		// Reload configurations
		s.webhookManager.LoadWebhookConfigs()

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    config,
		})

	default:
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWebhookByID handles individual webhook operations
func (s *Server) handleWebhookByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Parse webhook ID from URL path
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/webhooks/"), "/")
	if len(pathParts) == 0 || pathParts[0] == "" {
		SendJSONError(w, "Webhook ID is required", http.StatusBadRequest)
		return
	}

	webhookIDStr := pathParts[0]
	webhookID := 0
	if _, err := fmt.Sscanf(webhookIDStr, "%d", &webhookID); err != nil {
		SendJSONError(w, "Invalid webhook ID", http.StatusBadRequest)
		return
	}

	// Handle different sub-paths
	switch {
	case len(pathParts) == 1: // /api/webhooks/{id}
		switch r.Method {
		case http.MethodGet:
			// Get specific webhook configuration
			config, err := s.messageStore.GetWebhookConfig(webhookID)
			if err != nil {
				SendJSONError(w, fmt.Sprintf("Webhook not found: %v", err), http.StatusNotFound)
				return
			}

			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data":    config,
			})

		case http.MethodPut:
			// Update webhook configuration
			var config types.WebhookConfig
			if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
				SendJSONError(w, "Invalid request format", http.StatusBadRequest)
				return
			}

			config.ID = webhookID // Ensure ID matches URL

			fmt.Printf("Updating webhook %d with %d triggers\n", webhookID, len(config.Triggers))
			for i, trigger := range config.Triggers {
				fmt.Printf("  Trigger %d: type=%s, value=%s, match=%s, enabled=%t\n",
					i, trigger.TriggerType, trigger.TriggerValue, trigger.MatchType, trigger.Enabled)
			}

			// Validate configuration
			if err := s.webhookManager.ValidateWebhookConfig(&config); err != nil {
				SendJSONError(w, err.Error(), http.StatusBadRequest)
				return
			}

			// Update configuration
			if err := s.messageStore.UpdateWebhookConfig(&config); err != nil {
				SendJSONError(w, fmt.Sprintf("Failed to update webhook config: %v", err), http.StatusInternalServerError)
				return
			}

			// Reload configurations
			s.webhookManager.LoadWebhookConfigs()

			fmt.Printf("Successfully updated webhook %d\n", webhookID)

			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data":    config,
			})

		case http.MethodDelete:
			// Delete webhook configuration
			if err := s.messageStore.DeleteWebhookConfig(webhookID); err != nil {
				SendJSONError(w, fmt.Sprintf("Failed to delete webhook config: %v", err), http.StatusInternalServerError)
				return
			}

			// Reload configurations
			s.webhookManager.LoadWebhookConfigs()

			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"message": "Webhook deleted successfully",
			})

		default:
			SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		}

	case len(pathParts) == 2 && pathParts[1] == "test": // /api/webhooks/{id}/test
		if r.Method != http.MethodPost {
			SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Get webhook configuration
		config, err := s.messageStore.GetWebhookConfig(webhookID)
		if err != nil {
			SendJSONError(w, fmt.Sprintf("Webhook not found: %v", err), http.StatusNotFound)
			return
		}

		// Test webhook
		if err := s.webhookManager.TestWebhook(config); err != nil {
			SendJSONError(w, fmt.Sprintf("Webhook test failed: %v", err), http.StatusInternalServerError)
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Webhook test successful",
		})

	case len(pathParts) == 2 && pathParts[1] == "logs": // /api/webhooks/{id}/logs
		if r.Method != http.MethodGet {
			SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Get webhook logs
		logs, err := s.messageStore.GetWebhookLogs(webhookID, 100) // Limit to 100 recent logs
		if err != nil {
			SendJSONError(w, fmt.Sprintf("Failed to get webhook logs: %v", err), http.StatusInternalServerError)
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    logs,
		})

	case len(pathParts) == 2 && pathParts[1] == "enable": // /api/webhooks/{id}/enable
		if r.Method != http.MethodPost {
			SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse request body to get enabled status
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			SendJSONError(w, "Invalid request format", http.StatusBadRequest)
			return
		}

		// Get current config
		config, err := s.messageStore.GetWebhookConfig(webhookID)
		if err != nil {
			SendJSONError(w, fmt.Sprintf("Webhook not found: %v", err), http.StatusNotFound)
			return
		}

		// Update enabled status
		config.Enabled = req.Enabled
		if err := s.messageStore.UpdateWebhookConfig(config); err != nil {
			SendJSONError(w, fmt.Sprintf("Failed to update webhook config: %v", err), http.StatusInternalServerError)
			return
		}

		// Reload configurations
		s.webhookManager.LoadWebhookConfigs()

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Webhook %s successfully", map[bool]string{true: "enabled", false: "disabled"}[req.Enabled]),
			"data":    config,
		})

	default:
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWebhookLogs handles webhook logs endpoint
func (s *Server) handleWebhookLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// Get all webhook logs
	logs, err := s.messageStore.GetWebhookLogs(0, 100) // Get last 100 logs for all webhooks
	if err != nil {
		SendJSONError(w, fmt.Sprintf("Failed to get webhook logs: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    logs,
	})
}

// handleDownloadMedia handles single message media download (POST /api/download)
func (s *Server) handleDownloadMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		MessageID string `json:"message_id"`
		ChatJID   string `json:"chat_jid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MessageID == "" || req.ChatJID == "" {
		SendJSONError(w, "message_id and chat_jid are required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.client.MediaRetryTimeout()+60*time.Second)
	defer cancel()

	res, err := s.client.DownloadMedia(ctx, s.mediaStore, s.messageStore, req.MessageID, req.ChatJID, s.storeDir)
	if err != nil {
		SendJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}

	SendJSON(w, map[string]interface{}{
		"success":      true,
		"path":         res.Path,
		"abs_path":     res.AbsPath,
		"filename":     res.Filename,
		"media_type":   res.MediaType,
		"mimetype":     res.Mimetype,
		"cached":       res.Cached,
		"size":         res.Size,
		"sha256":       res.SHA256,
		"download_url": s.publicBaseURL + "/api/media/" + strings.TrimPrefix(filepath.ToSlash(res.Path), "media/"),
	})
}

// BatchDownloadRequest defines options for POST /api/download/batch
type BatchDownloadRequest struct {
	Chat           string `json:"chat"`
	MediaType      string `json:"media_type"`
	After          string `json:"after"`
	Before         string `json:"before"`
	Limit          int    `json:"limit"`
	SkipCached     *bool  `json:"skip_cached"`
	RetryFailed    bool   `json:"retry_failed"`
	SkipMediaRetry bool   `json:"skip_media_retry"`
}

// handleDownloadBatch starts an asynchronous batch download job (POST /api/download/batch)
func (s *Server) handleDownloadBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req BatchDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Chat) == "" {
		SendJSONError(w, "chat identifier is required", http.StatusBadRequest)
		return
	}

	// Only 1 batch job at a time
	if activeJob, _ := s.mediaStore.GetActiveJob(); activeJob != nil {
		SendJSONError(w, fmt.Sprintf("A batch download job (%s) is already running", activeJob.ID), http.StatusConflict)
		return
	}

	limit := req.Limit
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}

	skipCached := true
	if req.SkipCached != nil {
		skipCached = *req.SkipCached
	}

	for name, value := range map[string]string{"after": req.After, "before": req.Before} {
		if _, err := database.NormalizeTimeFilter(value); err != nil {
			SendJSONError(w, fmt.Sprintf("invalid %s: %v", name, err), http.StatusBadRequest)
			return
		}
	}

	// media_type selects the virtual "voice" type (audio with is_ptt) or plain audio.
	// Only messages received since schema v1 carry is_ptt, so "voice" finds nothing in
	// older history even where those messages are in fact voice notes.
	var voiceOnly bool
	switch strings.ToLower(strings.TrimSpace(req.MediaType)) {
	case "", "audio":
	case "voice":
		voiceOnly = true
	default:
		SendJSONError(w, fmt.Sprintf("unsupported media_type %q (use \"audio\" or \"voice\")", req.MediaType), http.StatusBadRequest)
		return
	}

	resolvedJIDs, err := whatsapp.ResolveChatJIDs(req.Chat, s.storeDir)
	if err != nil {
		SendJSONError(w, fmt.Sprintf("Failed to resolve chat JIDs: %v", err), http.StatusBadRequest)
		return
	}

	messages, err := s.messageStore.ListAudioMessages(resolvedJIDs, req.After, req.Before, limit, voiceOnly)
	if err != nil {
		SendJSONError(w, fmt.Sprintf("Failed to list audio messages: %v", err), http.StatusInternalServerError)
		return
	}

	jobID := fmt.Sprintf("job_%s_%03d", time.Now().UTC().Format("20060102_150405"), time.Now().UnixNano()%1000)
	reqJSON, _ := json.Marshal(req)

	job := &database.DownloadJob{
		ID:        jobID,
		Params:    string(reqJSON),
		Status:    "running",
		Total:     len(messages),
		StartedAt: time.Now().UTC(),
	}
	if err := s.mediaStore.CreateJob(job); err != nil {
		SendJSONError(w, fmt.Sprintf("Failed to create download job: %v", err), http.StatusInternalServerError)
		return
	}

	// Run job in background
	go s.runBatchDownload(jobID, req.Chat, resolvedJIDs, messages, skipCached, req.RetryFailed, req.SkipMediaRetry)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"job_id":        jobID,
		"chat":          req.Chat,
		"resolved_jids": resolvedJIDs,
		"folder":        filepath.ToSlash(filepath.Join("media", whatsapp.ChatFolderKey(resolvedJIDs))),
		"total":         len(messages),
		"status_url":    s.publicBaseURL + "/api/download/batch/" + jobID,
	})
}

// runBatchDownload executes the batch download sequentially with pace control
func (s *Server) runBatchDownload(
	jobID, chat string,
	resolvedJIDs []string,
	messages []database.MediaInfo,
	skipCached, retryFailed, skipMediaRetry bool,
) {
	done := 0
	failed := 0
	skipped := 0

	pace := time.Duration(s.paceMs) * time.Millisecond
	if pace <= 0 {
		pace = 250 * time.Millisecond
	}

	folderKey := whatsapp.ChatFolderKey(resolvedJIDs)
	outDir := filepath.Join(s.storeDir, "media", folderKey)
	_ = os.MkdirAll(outDir, 0755)

	for _, msg := range messages {
		// Check cancellation
		curJob, err := s.mediaStore.GetJob(jobID)
		if err != nil || curJob == nil || curJob.Status == "cancelled" {
			_ = s.mediaStore.FinishJob(jobID, "cancelled")
			return
		}

		rec, _ := s.mediaStore.GetDownload(msg.ID, msg.ChatJID)
		if rec == nil {
			if otherRec, _ := s.mediaStore.GetDownloadAnyChat(msg.ID); otherRec != nil {
				_ = s.mediaStore.RecordDownload(&database.MediaDownloadRecord{
					MessageID:  msg.ID,
					ChatJID:    msg.ChatJID,
					LocalPath:  otherRec.LocalPath,
					SHA256:     otherRec.SHA256,
					DirectPath: otherRec.DirectPath,
					Status:     otherRec.Status,
					Error:      otherRec.Error,
				})
				rec = otherRec
			}
		}

		if skipCached && rec != nil && rec.Status == database.DownloadStatusOK && rec.LocalPath != "" {
			fullPath := filepath.Join(s.storeDir, rec.LocalPath)
			if fi, err := os.Stat(fullPath); err == nil && fi.Size() > 0 {
				skipped++
				_ = s.mediaStore.UpdateJobProgress(jobID, done, failed, skipped, msg.ID)
				continue
			}
		}

		// Permanent failures (not on phone, re-keyed on phone, incomplete metadata) can't
		// succeed on a re-run; skip them so no retry request reaches a phone again.
		// Also skip failed records if skipMediaRetry is on and retryFailed is not set.
		if !retryFailed && rec != nil && (database.IsPermanentDownloadFailure(rec.Status) || (skipMediaRetry && rec.Status == database.DownloadStatusFailed)) {
			skipped++
			_ = s.mediaStore.UpdateJobProgress(jobID, done, failed, skipped, msg.ID)
			continue
		}

		timeout := s.client.MediaRetryTimeout() + 30*time.Second
		if skipMediaRetry {
			timeout = 15 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		_, err = s.client.DownloadMediaWithOptions(ctx, s.mediaStore, s.messageStore, msg.ID, msg.ChatJID, s.storeDir, whatsapp.DownloadOptions{
			SkipMediaRetry: skipMediaRetry,
		})
		cancel()

		if err != nil {
			failed++
		} else {
			done++
		}
		_ = s.mediaStore.UpdateJobProgress(jobID, done, failed, skipped, msg.ID)

		time.Sleep(pace)
	}

	// Write index.json at end
	s.writeBatchIndexJSON(outDir, resolvedJIDs)

	_ = s.mediaStore.FinishJob(jobID, "completed")
}

// writeBatchIndexJSON rebuilds index.json in the chat's media directory from every audio
// message of the chat that has a media.db record, so earlier jobs' results are kept
func (s *Server) writeBatchIndexJSON(outDir string, chatJIDs []string) {
	messages, err := s.messageStore.ListAudioMessages(chatJIDs, "", "", 0, false)
	if err != nil {
		fmt.Printf("Failed to list audio messages for index.json: %v\n", err)
		return
	}

	type IndexEntry struct {
		MessageID string `json:"message_id"`
		ChatJID   string `json:"chat_jid"`
		Timestamp string `json:"timestamp"`
		FromMe    bool   `json:"from_me"`
		File      string `json:"file,omitempty"`
		Status    string `json:"status"`
		Bytes     int64  `json:"bytes,omitempty"`
		SHA256    string `json:"sha256,omitempty"`
		Error     string `json:"error,omitempty"`
	}

	entries := []IndexEntry{}
	for _, msg := range messages {
		rec, _ := s.mediaStore.GetDownload(msg.ID, msg.ChatJID)
		if rec == nil {
			continue // never attempted
		}
		entry := IndexEntry{
			MessageID: msg.ID,
			ChatJID:   msg.ChatJID,
			Timestamp: msg.Timestamp.UTC().Format("2006-01-02 15:04:05+00:00"),
			FromMe:    msg.IsFromMe,
			Status:    rec.Status,
			SHA256:    rec.SHA256,
			Error:     rec.Error,
		}
		if rec.LocalPath != "" {
			entry.File = filepath.Base(rec.LocalPath)
			if fi, err := os.Stat(filepath.Join(s.storeDir, rec.LocalPath)); err == nil {
				entry.Bytes = fi.Size()
			}
		}
		entries = append(entries, entry)
	}

	indexPath := filepath.Join(outDir, "index.json")
	tmpPath := indexPath + ".part"
	data, err := json.MarshalIndent(entries, "", "  ")
	if err == nil {
		if err := os.WriteFile(tmpPath, data, 0644); err == nil {
			_ = os.Rename(tmpPath, indexPath)
		}
	}
}

// handleBatchJobByID handles polling and cancelling batch download jobs (GET/DELETE /api/download/batch/{id})
func (s *Server) handleBatchJobByID(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimPrefix(r.URL.Path, "/api/download/batch/")
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		SendJSONError(w, "job ID is required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		job, err := s.mediaStore.GetJob(jobID)
		if err != nil {
			SendJSONError(w, fmt.Sprintf("Failed to get job: %v", err), http.StatusInternalServerError)
			return
		}
		if job == nil {
			SendJSONError(w, "job not found", http.StatusNotFound)
			return
		}
		SendJSON(w, job)

	case http.MethodDelete:
		cancelled, err := s.mediaStore.CancelJob(jobID)
		if err != nil {
			SendJSONError(w, fmt.Sprintf("Failed to cancel job: %v", err), http.StatusInternalServerError)
			return
		}
		if !cancelled {
			SendJSONError(w, "no running job with that ID", http.StatusNotFound)
			return
		}
		SendJSON(w, map[string]interface{}{
			"success": true,
			"message": "job cancelled",
		})

	default:
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// servableMediaTypes maps the only file extensions /api/media/ may serve to their content types
var servableMediaTypes = map[string]string{
	".ogg": "audio/ogg; codecs=opus",
	".mp3": "audio/mpeg",
}

// resolveMediaPath maps a request subpath to a file inside mediaRoot. It rejects traversal,
// absolute paths, symlinks leaving mediaRoot, and any extension other than .ogg/.mp3, so the
// databases and other files in the store directory can never be served.
func resolveMediaPath(mediaRoot, subpath string) (string, string, bool) {
	if subpath == "" || strings.Contains(subpath, "\\") {
		return "", "", false
	}
	cleanPath := filepath.Clean(subpath)
	if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
		return "", "", false
	}
	contentType, ok := servableMediaTypes[strings.ToLower(filepath.Ext(cleanPath))]
	if !ok {
		return "", "", false
	}

	rootAbs, err := filepath.EvalSymlinks(mediaRoot)
	if err != nil {
		return "", "", false
	}
	targetAbs, err := filepath.EvalSymlinks(filepath.Join(mediaRoot, cleanPath))
	if err != nil {
		return "", "", false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", false
	}
	if fi, err := os.Stat(targetAbs); err != nil || !fi.Mode().IsRegular() {
		return "", "", false
	}
	return targetAbs, contentType, true
}

// handleServeMedia streams downloaded audio files from <store>/media (GET /api/media/{path...})
func (s *Server) handleServeMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	subpath := strings.TrimPrefix(r.URL.Path, "/api/media/")
	targetPath, contentType, ok := resolveMediaPath(filepath.Join(s.storeDir, "media"), subpath)
	if !ok {
		SendJSONError(w, "media file not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", contentType)
	// ?download=1 asks the browser to save rather than play. A header is the only way to do
	// this for the UI: it runs on another port, and browsers ignore <a download> on
	// cross-origin links. The filename comes from our own sanitised path; FormatMediaType
	// still quotes it properly.
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition",
			mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(targetPath)}))
	}
	http.ServeFile(w, r, targetPath)
}

// maxChatPickerLimit caps what one request can pull out of a 3,900-row chat list.
const (
	defaultChatLimit = 50
	maxChatLimit     = 200
)

// handleChats lists chats for the UI's trigger picker (GET /api/chats).
//
// Query: q (case-insensitive substring of name or JID), type (all|contact|group),
// limit (default 50, capped at 200).
func (s *Server) handleChats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	kind := r.URL.Query().Get("type")
	switch kind {
	case "", database.ChatKindAll:
		kind = database.ChatKindAll
	case database.ChatKindContact, database.ChatKindGroup:
	default:
		SendJSONError(w, "type must be all, contact or group", http.StatusBadRequest)
		return
	}

	limit := defaultChatLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			SendJSONError(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	if limit > maxChatLimit {
		limit = maxChatLimit
	}

	chats, err := s.messageStore.ListChats(strings.TrimSpace(r.URL.Query().Get("q")), kind, limit)
	if err != nil {
		SendJSONError(w, fmt.Sprintf("Failed to list chats: %v", err), http.StatusInternalServerError)
		return
	}

	SendJSON(w, map[string]interface{}{
		"success": true,
		"data":    chats,
	})
}

// handleHealth reports what the RUNNING bridge is, not what was built (GET /api/health).
//
// This exists because "Built" and "Up/healthy" both lie after a redeploy that started a
// stale container instead of replacing it — which has happened twice (§2.10, §2.15). The
// schema version here is the honest answer to "did my deploy actually take effect?".
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	schemaVersion, err := s.messageStore.SchemaVersion()
	if err != nil {
		SendJSONError(w, fmt.Sprintf("Failed to read schema version: %v", err), http.StatusInternalServerError)
		return
	}

	autoDownload := map[string]interface{}{"enabled": false}
	if s.autoDownload != nil {
		autoDownload = map[string]interface{}{
			"enabled":     true,
			"workers":     s.autoDownload.Workers(),
			"queue_depth": s.autoDownload.QueueDepth(),
		}
	}

	connected := false
	if s.client != nil {
		connected = s.client.IsConnected()
	}

	SendJSON(w, map[string]interface{}{
		"success":            true,
		"status":             "ok",
		"schema_version":     schemaVersion,
		"whatsapp_connected": connected,
		"auto_download":      autoDownload,
	})
}

const (
	defaultDownloadsLimit = 50
	maxDownloadsLimit     = 500
)

// downloadStatuses are the values ?status= accepts; "all" lifts the filter.
var downloadStatuses = map[string]bool{
	database.DownloadStatusOK:                 true,
	database.DownloadStatusFailed:             true,
	database.DownloadStatusRetryTimeout:       true,
	database.DownloadStatusNotOnPhone:         true,
	database.DownloadStatusRekeyedOnPhone:     true,
	database.DownloadStatusIncompleteMetadata: true,
}

// downloadItem is one row of GET /api/downloads.
type downloadItem struct {
	MessageID    string `json:"message_id"`
	ChatJID      string `json:"chat_jid"`
	ChatName     string `json:"chat_name,omitempty"`
	Sender       string `json:"sender,omitempty"`
	IsFromMe     bool   `json:"is_from_me"`
	Timestamp    string `json:"timestamp,omitempty"`
	Seconds      int    `json:"seconds,omitempty"`
	IsPTT        bool   `json:"is_ptt"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	DownloadedAt string `json:"downloaded_at,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	// MediaPath is relative to /api/media/. The UI builds its own URL from it, because
	// DownloadURL is built from PUBLIC_BASE_URL — the bridge's container DNS name, which is
	// right for the MCP server and other services on the Docker network but unresolvable
	// from a user's browser.
	MediaPath   string `json:"media_path,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	// FileMissing marks an ok record whose file is no longer on disk.
	FileMissing bool `json:"file_missing,omitempty"`
}

// handleDownloads lists downloaded media with the message behind each file
// (GET /api/downloads).
//
// Query: chat (phone or any JID; expanded to the contact's phone and LID chats), after and
// before (the message's send time, not the download time), status (default ok; "all"
// for any), limit (default 50, cap 500), offset. Newest message first.
func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()

	status := q.Get("status")
	switch {
	case status == "":
		status = database.DownloadStatusOK
	case status == "all":
		status = ""
	case !downloadStatuses[status]:
		SendJSONError(w, fmt.Sprintf("unknown status %q", status), http.StatusBadRequest)
		return
	}

	limit, err := positiveInt(q.Get("limit"), defaultDownloadsLimit)
	if err != nil {
		SendJSONError(w, "limit must be a positive integer", http.StatusBadRequest)
		return
	}
	if limit > maxDownloadsLimit {
		limit = maxDownloadsLimit
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			SendJSONError(w, "offset must be a non-negative integer", http.StatusBadRequest)
			return
		}
	}

	after, hasAfter, err := database.ParseTimeFilter(q.Get("after"))
	if err != nil {
		SendJSONError(w, "after: "+err.Error(), http.StatusBadRequest)
		return
	}
	before, hasBefore, err := database.ParseTimeFilter(q.Get("before"))
	if err != nil {
		SendJSONError(w, "before: "+err.Error(), http.StatusBadRequest)
		return
	}

	var chatJIDs []string
	if chat := strings.TrimSpace(q.Get("chat")); chat != "" {
		chatJIDs, err = whatsapp.ResolveChatJIDs(chat, s.storeDir)
		if err != nil {
			SendJSONError(w, "chat: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	records, err := s.mediaStore.ListDownloadRecords(chatJIDs, status)
	if err != nil {
		SendJSONError(w, fmt.Sprintf("Failed to list downloads: %v", err), http.StatusInternalServerError)
		return
	}

	wanted := make(map[string][]string, len(records))
	for _, rec := range records {
		wanted[rec.MessageID] = append(wanted[rec.MessageID], rec.ChatJID)
	}
	meta, err := s.messageStore.GetDownloadMetadata(wanted)
	if err != nil {
		SendJSONError(w, fmt.Sprintf("Failed to read message metadata: %v", err), http.StatusInternalServerError)
		return
	}

	// Filter on the message's send time. A record whose message row is gone cannot be
	// dated, so a date filter excludes it rather than guessing.
	type row struct {
		rec  database.MediaDownloadRecord
		meta database.DownloadMetadata
		has  bool
	}
	rows := make([]row, 0, len(records))
	for _, rec := range records {
		m, has := meta[database.DownloadKey(rec.MessageID, rec.ChatJID)]
		if (hasAfter || hasBefore) && !has {
			continue
		}
		if hasAfter && m.Timestamp.Before(after) {
			continue
		}
		if hasBefore && !m.Timestamp.Before(before) {
			continue
		}
		rows = append(rows, row{rec: rec, meta: m, has: has})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].meta.Timestamp.Equal(rows[j].meta.Timestamp) {
			return rows[i].meta.Timestamp.After(rows[j].meta.Timestamp)
		}
		return rows[i].rec.MessageID > rows[j].rec.MessageID
	})

	total := len(rows)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := rows[offset:end]

	chatSet := make(map[string]bool)
	for _, r := range page {
		chatSet[r.rec.ChatJID] = true
	}
	chatList := make([]string, 0, len(chatSet))
	for jid := range chatSet {
		chatList = append(chatList, jid)
	}
	names, err := s.messageStore.GetChatNames(chatList)
	if err != nil {
		SendJSONError(w, fmt.Sprintf("Failed to read chat names: %v", err), http.StatusInternalServerError)
		return
	}

	items := make([]downloadItem, 0, len(page))
	for _, r := range page {
		item := downloadItem{
			MessageID: r.rec.MessageID,
			ChatJID:   r.rec.ChatJID,
			ChatName:  names[r.rec.ChatJID],
			Status:    r.rec.Status,
			Error:     r.rec.Error,
		}
		if r.has {
			item.Sender = r.meta.Sender
			item.IsFromMe = r.meta.IsFromMe
			item.Timestamp = r.meta.Timestamp.UTC().Format(time.RFC3339)
			item.Seconds = r.meta.Seconds
			item.IsPTT = r.meta.IsPTT
		}
		if r.rec.DownloadedAt != nil {
			item.DownloadedAt = r.rec.DownloadedAt.UTC().Format(time.RFC3339)
		}
		if r.rec.Status == database.DownloadStatusOK && r.rec.LocalPath != "" {
			if fi, statErr := os.Stat(filepath.Join(s.storeDir, r.rec.LocalPath)); statErr == nil && fi.Mode().IsRegular() {
				item.SizeBytes = fi.Size()
				item.MediaPath = strings.TrimPrefix(filepath.ToSlash(r.rec.LocalPath), "media/")
				item.DownloadURL = s.publicBaseURL + "/api/media/" + item.MediaPath
			} else {
				item.FileMissing = true
			}
		}
		items = append(items, item)
	}

	SendJSON(w, map[string]interface{}{
		"success": true,
		"data":    items,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

// positiveInt parses raw as a positive integer, returning def when raw is empty.
func positiveInt(raw string, def int) (int, error) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("not a positive integer: %q", raw)
	}
	return n, nil
}
