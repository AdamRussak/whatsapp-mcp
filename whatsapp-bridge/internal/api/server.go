package api

import (
	"fmt"
	"net/http"

	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/webhook"
	"whatsapp-bridge/internal/whatsapp"
)

// Server represents the HTTP API server
type Server struct {
	client         *whatsapp.Client
	messageStore   *database.MessageStore
	mediaStore     *database.MediaStore
	webhookManager *webhook.Manager
	port           int
	storeDir       string
	paceMs         int
	publicBaseURL  string
}

// NewServer creates a new API server instance
func NewServer(
	client *whatsapp.Client,
	messageStore *database.MessageStore,
	mediaStore *database.MediaStore,
	webhookManager *webhook.Manager,
	port int,
	storeDir string,
	paceMs int,
	publicBaseURL string,
) *Server {
	return &Server{
		client:         client,
		messageStore:   messageStore,
		mediaStore:     mediaStore,
		webhookManager: webhookManager,
		port:           port,
		storeDir:       storeDir,
		paceMs:         paceMs,
		publicBaseURL:  publicBaseURL,
	}
}

// Start starts the HTTP server
func (s *Server) Start() {
	// Register handlers
	s.registerHandlers()

	// Start the server
	serverAddr := fmt.Sprintf(":%d", s.port)
	fmt.Printf("Starting REST API server on %s...\n", serverAddr)

	// Run server in a goroutine so it doesn't block
	go func() {
		if err := http.ListenAndServe(serverAddr, nil); err != nil {
			fmt.Printf("REST API server error: %v\n", err)
		}
	}()
}

// registerHandlers registers all HTTP handlers
func (s *Server) registerHandlers() {
	// Message sending endpoint
	http.HandleFunc("/api/send", CorsMiddleware(s.handleSendMessage))

	// Media download endpoints
	http.HandleFunc("/api/download", CorsMiddleware(s.handleDownloadMedia))
	http.HandleFunc("/api/download/batch", CorsMiddleware(s.handleDownloadBatch))
	http.HandleFunc("/api/download/batch/", CorsMiddleware(s.handleBatchJobByID))
	http.HandleFunc("/api/media/", CorsMiddleware(s.handleServeMedia))

	// Chat list, for the UI's trigger picker
	http.HandleFunc("/api/chats", CorsMiddleware(s.handleChats))

	// Webhook management endpoints
	http.HandleFunc("/api/webhooks", CorsMiddleware(s.handleWebhooks))
	http.HandleFunc("/api/webhooks/", CorsMiddleware(s.handleWebhookByID))
	http.HandleFunc("/api/webhook-logs", CorsMiddleware(s.handleWebhookLogs))
}
