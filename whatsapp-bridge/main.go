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

	// Initialize database
	messageStore, err := database.NewMessageStore()
	if err != nil {
		logger.Errorf("Failed to initialize message store: %v", err)
		return
	}
	defer messageStore.Close()

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
	webhookManager := webhook.NewManager(messageStore, logger)
	err = webhookManager.LoadWebhookConfigs()
	if err != nil {
		logger.Errorf("Failed to load webhook configs: %v", err)
		return
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
	// Disconnect client
	client.Disconnect()
}
