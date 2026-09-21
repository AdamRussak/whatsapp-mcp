package whatsapp

import (
	"context"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestDeleteSessionIfExists_NilStore(t *testing.T) {
	client := &Client{
		Client: nil,
		logger: waLog.Noop,
	}

	err := client.DeleteSessionIfExists(context.Background())
	if err != nil {
		t.Fatalf("Expected nil error for nil Store, got: %v", err)
	}
}

func TestDeleteSessionIfExists_NilID(t *testing.T) {
	container, err := sqlstore.New(context.Background(), "sqlite3", "file::memory:?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("Failed to create in-memory store: %v", err)
	}
	defer container.Close()

	deviceStore := container.NewDevice()
	if deviceStore.ID != nil {
		t.Fatalf("Expected newly created device to have nil ID, got: %v", deviceStore.ID)
	}

	waClient := whatsmeow.NewClient(deviceStore, waLog.Noop)
	client := &Client{
		Client: waClient,
		logger: waLog.Noop,
	}

	// Calling DeleteSessionIfExists when ID is nil should skip DB deletion and return nil without ErrDeviceIDMustBeSet
	err = client.DeleteSessionIfExists(context.Background())
	if err != nil {
		t.Fatalf("Expected nil error when ID is nil, got: %v", err)
	}
}

func TestDeleteSessionIfExists_WithID(t *testing.T) {
	container, err := sqlstore.New(context.Background(), "sqlite3", "file::memory:?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("Failed to create in-memory store: %v", err)
	}
	defer container.Close()

	deviceStore := container.NewDevice()
	testJID := types.NewJID("1234567890", types.DefaultUserServer)
	deviceStore.ID = &testJID
	deviceStore.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             make([]byte, 10),
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}

	// Save the device to the database so it exists when DeleteDevice is called
	if err := deviceStore.Save(context.Background()); err != nil {
		t.Fatalf("Failed to save device: %v", err)
	}

	waClient := whatsmeow.NewClient(deviceStore, waLog.Noop)
	client := &Client{
		Client: waClient,
		logger: waLog.Noop,
	}

	// Calling DeleteSessionIfExists when ID is set should delete the device and clear ID
	err = client.DeleteSessionIfExists(context.Background())
	if err != nil {
		t.Fatalf("Expected nil error when ID is set and deleted, got: %v", err)
	}

	if deviceStore.ID != nil {
		t.Errorf("Expected deviceStore.ID to be nil after deletion, got: %v", deviceStore.ID)
	}
}
