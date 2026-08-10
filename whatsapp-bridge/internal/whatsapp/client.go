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
	"go.mau.fi/whatsmeow/types/events"
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

	container, err := sqlstore.New(context.Background(), "sqlite3", "file:store/whatsapp.db?_foreign_keys=on&_busy_timeout=5000", dbLog)
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

// DeleteSessionIfExists deletes the device session from the database if a valid device ID exists.
// If the device ID is nil (not yet paired or already deleted), it skips database deletion and returns nil.
func (c *Client) DeleteSessionIfExists(ctx context.Context) error {
	if c == nil || c.Client == nil || c.Store == nil {
		return nil
	}
	if c.Store.ID != nil {
		if err := c.Store.Delete(ctx); err != nil {
			return fmt.Errorf("failed to delete session: %w", err)
		}
		c.logger.Infof("Session deleted successfully")
		return nil
	}
	c.logger.Warnf("No active session to delete (device not yet paired or ID is nil)")
	return nil
}

// HandleConnectionEvent processes connection-level events like ClientOutdated, LoggedOut, and ConnectFailure.
// onExit callback is invoked when the application should exit due to permanent disconnection or logout.
func (c *Client) HandleConnectionEvent(ctx context.Context, evt interface{}, onExit func(int)) {
	switch v := evt.(type) {
	case *events.Connected:
		c.logger.Infof("Connected to WhatsApp")

	case *events.LoggedOut:
		c.logger.Warnf("Device logged out. Deleting session and exiting to generate new QR code on restart...")
		if err := c.DeleteSessionIfExists(ctx); err != nil {
			c.logger.Errorf("Failed to delete session after logout: %v", err)
		}
		if onExit != nil {
			onExit(1)
		}

	case *events.ClientOutdated:
		c.logger.Errorf("Client is outdated (405). Deleting session and exiting to generate new QR code on restart...")
		if err := c.DeleteSessionIfExists(ctx); err != nil {
			c.logger.Errorf("Failed to delete session: %v", err)
		}
		if onExit != nil {
			onExit(1)
		}

	case *events.ConnectFailure:
		c.logger.Errorf("Connection failure: %v (reason: %s)", v.Reason, v.Message)
	}
}
