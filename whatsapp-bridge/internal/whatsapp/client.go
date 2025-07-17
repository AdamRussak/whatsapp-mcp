package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/mdp/qrterminal"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Client wraps the WhatsApp client with additional functionality
type Client struct {
	*whatsmeow.Client
	logger waLog.Logger
}

// NewClient creates a new WhatsApp client instance
func NewClient(logger waLog.Logger) (*Client, error) {
	// Create database connection for storing session data
	dbLog := waLog.Stdout("Database", "INFO", true)

	// Create directory for database if it doesn't exist
	if err := os.MkdirAll("store", 0755); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %v", err)
	}

	container, err := sqlstore.New(context.Background(), "sqlite3", "file:store/whatsapp.db?_foreign_keys=on", dbLog)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %v", err)
	}

	// Get device store - This contains session information
	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		if err == sql.ErrNoRows {
			// No device exists, create one
			deviceStore = container.NewDevice()
			logger.Infof("Created new device")
		} else {
			return nil, fmt.Errorf("failed to get device: %v", err)
		}
	}

	// Create client instance
	client := whatsmeow.NewClient(deviceStore, logger)
	if client == nil {
		return nil, fmt.Errorf("failed to create WhatsApp client")
	}

	return &Client{
		Client: client,
		logger: logger,
	}, nil
}

// Connect establishes connection to WhatsApp with QR code handling if needed
func (c *Client) Connect() error {
	// Create channel to track connection success
	connected := make(chan bool, 1)

	if c.Store.ID == nil {
		// No ID stored, this is a new client, need to pair with phone
		qrChan, _ := c.GetQRChannel(context.Background())
		err := c.Client.Connect()
		if err != nil {
			return fmt.Errorf("failed to connect: %v", err)
		}

		// Print QR code for pairing with phone
		for evt := range qrChan {
			if evt.Event == "code" {
				fmt.Println("\nScan this QR code with your WhatsApp app:")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			} else if evt.Event == "success" {
				connected <- true
				break
			}
		}

		// Wait for connection
		select {
		case <-connected:
			fmt.Println("\nSuccessfully connected and authenticated!")
		case <-time.After(3 * time.Minute):
			return fmt.Errorf("timeout waiting for QR code scan")
		}
	} else {
		// Already logged in, just connect
		err := c.Client.Connect()
		if err != nil {
			return fmt.Errorf("failed to connect: %v", err)
		}
		connected <- true
	}

	// Wait a moment for connection to stabilize
	time.Sleep(2 * time.Second)

	if !c.IsConnected() {
		return fmt.Errorf("failed to establish stable connection")
	}

	c.logger.Infof("✓ Connected to WhatsApp!")
	return nil
}
