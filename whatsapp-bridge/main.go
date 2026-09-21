package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"whatsapp-bridge/internal/api"
	"whatsapp-bridge/internal/autodownload"
	"whatsapp-bridge/internal/config"
	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/retention"
	"whatsapp-bridge/internal/types"
	"whatsapp-bridge/internal/webhook"
	"whatsapp-bridge/internal/whatsapp"
)

func main() {
	// Set up logger
	logger := waLog.Stdout("Client", "INFO", true)
	logger.Infof("Starting WhatsApp client...")

	// Load configuration
	cfg := config.NewConfig()

	// Initialize database (this also applies schema migrations)
	messageStore, err := database.NewMessageStore()
	if err != nil {
		// Exit non-zero: never serve on a half-migrated database (§4.2)
		logger.Errorf("Failed to initialize message store: %v", err)
		os.Exit(1)
	}
	defer messageStore.Close()

	if v, err := messageStore.SchemaVersion(); err != nil {
		logger.Warnf("Failed to read schema version: %v", err)
	} else {
		logger.Infof("messages.db schema version %d", v)
	}

	// Initialize media store for download tracking (store/media.db)
	mediaStore, err := database.NewMediaStore(cfg.StoreDir)
	if err != nil {
		logger.Errorf("Failed to initialize media store: %v", err)
		return
	}
	defer mediaStore.Close()

	// A job still 'running' was cut off by a restart; without this it would block new batches
	if n, err := mediaStore.MarkInterruptedJobs(); err != nil {
		logger.Errorf("Failed to mark interrupted download jobs: %v", err)
	} else if n > 0 {
		logger.Warnf("Marked %d interrupted download job(s)", n)
	}

	// Create WhatsApp client
	client, err := whatsapp.NewClient(logger)
	if err != nil {
		logger.Errorf("Failed to create WhatsApp client: %v", err)
		return
	}
	client.SetMediaRetryTimeout(time.Duration(cfg.MediaRetryTimeoutS) * time.Second)

	// Initialize webhook manager
	webhookManager := webhook.NewManager(messageStore, logger, cfg.PublicBaseURL, cfg.StoreDir)
	err = webhookManager.LoadWebhookConfigs()
	if err != nil {
		logger.Errorf("Failed to load webhook configs: %v", err)
		return
	}

	// Auto-download: listeners are webhook configs with auto_download on, so the pool needs
	// no rules of its own — only a way to fetch. SkipMediaRetry is deliberate: live media is
	// seconds old and downloads directly, and it guarantees no retry receipt ever reaches a
	// phone for an item that arrives here (AUDIO_DOWNLOAD_PLAN.md §2.13, B1).
	var autoDownloader *autodownload.Manager
	if cfg.AutoDownloadEnabled {
		autoDownloader = autodownload.NewManager(
			voiceDownloader{client: client, mediaStore: mediaStore, messageStore: messageStore, storeDir: cfg.StoreDir},
			captureLogger{store: messageStore},
			logger,
			autodownload.Options{
				Workers:   cfg.AutoDownloadWorkers,
				QueueSize: cfg.AutoDownloadQueueSize,
				Timeout:   time.Duration(cfg.AutoDownloadTimeoutS) * time.Second,
			},
		)
		autoDownloader.Start()
		webhookManager.SetAutoDownloader(autoDownloader)
	} else {
		logger.Infof("Auto-download disabled (AUTO_DOWNLOAD=off); webhook rules still match and deliver")
	}

	// Retention: removes a listener's captures once its retention_days have passed. Every
	// listener defaults to keeping forever, so nothing is removed until one is told to.
	var sweeper *retention.Sweeper
	if cfg.RetentionEnabled {
		sweeper = retention.New(messageStore, mediaStore, cfg.StoreDir, logger, retention.Options{
			Interval:    time.Duration(cfg.RetentionIntervalMinutes) * time.Minute,
			MaxPerSweep: cfg.RetentionMaxPerSweep,
			DryRun:      cfg.RetentionDryRun,
		})
		sweeper.Start()
	} else {
		logger.Infof("Retention disabled (RETENTION=off); captured files are kept")
	}

	// Setup event handling for messages, history sync, and media retry
	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			// Process regular messages with webhook support
			client.HandleMessage(messageStore, webhookManager, v)

		case *events.HistorySync:
			// Process history sync events
			client.HandleHistorySync(messageStore, v)

		case *events.MediaRetry:
			// Dispatch media retry responses to waiting downloaders
			client.HandleMediaRetry(v)

		case *events.Connected, *events.LoggedOut, *events.ClientOutdated, *events.ConnectFailure:
			client.HandleConnectionEvent(context.Background(), v, os.Exit)
		}
	})

	// Connect to WhatsApp
	if err := client.Connect(); err != nil {
		logger.Errorf("Failed to connect to WhatsApp: %v", err)
		return
	}

	fmt.Println("\n✓ Connected to WhatsApp! Type 'help' for commands.")

	// Start REST API server with webhook and media download support
	server := api.NewServer(client, messageStore, mediaStore, webhookManager, cfg.APIPort, cfg.StoreDir, cfg.DownloadPaceMs, cfg.PublicBaseURL)
	if autoDownloader != nil {
		server.SetAutoDownloadStatus(autoDownloader)
	}
	if sweeper != nil {
		server.SetRetention(sweeper)
	}
	server.Start()

	// Create a channel to keep the main goroutine alive
	exitChan := make(chan os.Signal, 1)
	signal.Notify(exitChan, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("REST server is running. Press Ctrl+C to disconnect and exit.")

	// Wait for termination signal
	<-exitChan

	fmt.Println("Disconnecting...")
	// Shut the auto-download pool down before the client goes away: its workers download
	// over that same connection. Stop lets what is already downloading finish and drops
	// what is merely queued; stop_grace_period in docker-compose.yaml is what gives it
	// the room to do that instead of being SIGKILLed mid-file.
	if autoDownloader != nil {
		autoDownloader.Stop()
	}
	if sweeper != nil {
		sweeper.Stop()
	}
	// Disconnect client
	client.Disconnect()
}

// voiceDownloader adapts the WhatsApp client to autodownload.Downloader, binding the stores
// and the store directory the pool would otherwise have to know about.
type voiceDownloader struct {
	client       *whatsapp.Client
	mediaStore   *database.MediaStore
	messageStore *database.MessageStore
	storeDir     string
}

func (d voiceDownloader) DownloadVoice(ctx context.Context, messageID, chatJID string) (string, bool, error) {
	res, err := d.client.DownloadMediaWithOptions(
		ctx, d.mediaStore, d.messageStore, messageID, chatJID, d.storeDir,
		whatsapp.DownloadOptions{SkipMediaRetry: true},
	)
	if err != nil {
		return "", false, err
	}
	return res.Path, res.Cached, nil
}

// captureLogger writes each auto-download outcome into webhook_logs, next to the delivery
// logs of the same listener. It reuses that table rather than adding a second one: a
// capture and a delivery are two actions of one listener, and the UI already has a log view
// per listener — a download-only listener would otherwise show nothing anywhere.
type captureLogger struct{ store *database.MessageStore }

func (c captureLogger) RecordCapture(job autodownload.Job, ok bool, detail string, duration time.Duration) {
	status := "failed"
	if ok {
		status = "captured"
	}

	payload, err := json.Marshal(map[string]any{
		"status":      status,
		"detail":      detail,
		"duration_ms": duration.Milliseconds(),
		"rule":        job.RuleName,
	})
	if err != nil {
		payload = []byte("{}")
	}

	// delivered_at doubles as the success flag the log view reads; a failure leaves it NULL.
	var deliveredAt *time.Time
	if ok {
		now := time.Now()
		deliveredAt = &now
	}

	logErr := c.store.StoreWebhookLog(&types.WebhookLog{
		WebhookConfigID: job.RuleID,
		MessageID:       job.MessageID,
		ChatJID:         job.ChatJID,
		TriggerType:     job.TriggerType,
		TriggerValue:    job.TriggerValue,
		Payload:         string(payload),
		ResponseBody:    detail,
		AttemptCount:    1,
		DeliveredAt:     deliveredAt,
		EventType:       "auto_download",
	})
	if logErr != nil {
		fmt.Printf("Failed to log auto-download outcome for %s: %v\n", job.MessageID, logErr)
	}
}
