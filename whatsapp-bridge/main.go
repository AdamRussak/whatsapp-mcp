package main

import (
	"context"
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
	webhookManager := webhook.NewManager(messageStore, logger, cfg.PublicBaseURL)
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
