package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
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
	Chat        string `json:"chat"`
	MediaType   string `json:"media_type"`
	After       string `json:"after"`
	Before      string `json:"before"`
	Limit       int    `json:"limit"`
	SkipCached  *bool  `json:"skip_cached"`
	RetryFailed bool   `json:"retry_failed"`
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

	resolvedJIDs, err := whatsapp.ResolveChatJIDs(req.Chat, s.storeDir)
	if err != nil {
		SendJSONError(w, fmt.Sprintf("Failed to resolve chat JIDs: %v", err), http.StatusBadRequest)
		return
	}

	messages, err := s.messageStore.ListAudioMessages(resolvedJIDs, req.After, req.Before, limit)
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
	go s.runBatchDownload(jobID, req.Chat, resolvedJIDs, messages, skipCached, req.RetryFailed)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"job_id":        jobID,
		"chat":          req.Chat,
		"resolved_jids": resolvedJIDs,
		"total":         len(messages),
		"status_url":    s.publicBaseURL + "/api/download/batch/" + jobID,
	})
}

// runBatchDownload executes the batch download sequentially with pace control
func (s *Server) runBatchDownload(
	jobID, chat string,
	resolvedJIDs []string,
	messages []database.MediaInfo,
	skipCached, retryFailed bool,
) {
	done := 0
	failed := 0
	skipped := 0

	pace := time.Duration(s.paceMs) * time.Millisecond
	if pace <= 0 {
		pace = 250 * time.Millisecond
	}

	folderKey := chat
	if !strings.HasSuffix(chat, "@g.us") {
		for _, j := range resolvedJIDs {
			if strings.HasSuffix(j, "@s.whatsapp.net") {
				folderKey = strings.SplitN(j, "@", 2)[0]
				break
			}
		}
	}
	folderKey = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, folderKey)

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
		if skipCached && rec != nil && rec.Status == "ok" && rec.LocalPath != "" {
			fullPath := filepath.Join(s.storeDir, rec.LocalPath)
			if fi, err := os.Stat(fullPath); err == nil && fi.Size() > 0 {
				skipped++
				_ = s.mediaStore.UpdateJobProgress(jobID, done, failed, skipped, msg.ID)
				continue
			}
		}

		if !retryFailed && rec != nil && (rec.Status == "expired_not_on_phone" || rec.Status == "incomplete_metadata") {
			skipped++
			_ = s.mediaStore.UpdateJobProgress(jobID, done, failed, skipped, msg.ID)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		_, err = s.client.DownloadMedia(ctx, s.mediaStore, s.messageStore, msg.ID, msg.ChatJID, s.storeDir)
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
	s.writeBatchIndexJSON(outDir, messages)

	_ = s.mediaStore.FinishJob(jobID, "completed")
}

// writeBatchIndexJSON builds an index.json summary in the chat's media directory
func (s *Server) writeBatchIndexJSON(outDir string, messages []database.MediaInfo) {
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

	var entries []IndexEntry
	for _, msg := range messages {
		rec, _ := s.mediaStore.GetDownload(msg.ID, msg.ChatJID)
		entry := IndexEntry{
			MessageID: msg.ID,
			ChatJID:   msg.ChatJID,
			Timestamp: msg.Timestamp.UTC().Format("2006-01-02 15:04:05+00:00"),
			FromMe:    msg.IsFromMe,
			Status:    "unknown",
		}
		if rec != nil {
			entry.Status = rec.Status
			entry.SHA256 = rec.SHA256
			entry.Error = rec.Error
			if rec.LocalPath != "" {
				entry.File = filepath.Base(rec.LocalPath)
				fullPath := filepath.Join(s.storeDir, rec.LocalPath)
				if fi, err := os.Stat(fullPath); err == nil {
					entry.Bytes = fi.Size()
				}
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
		if err := s.mediaStore.FinishJob(jobID, "cancelled"); err != nil {
			SendJSONError(w, fmt.Sprintf("Failed to cancel job: %v", err), http.StatusInternalServerError)
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
	http.ServeFile(w, r, targetPath)
}
