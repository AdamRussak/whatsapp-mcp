package whatsapp

import (
	"context"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestHandleConnectionEvent_ClientOutdated_NilID(t *testing.T) {
	container, err := sqlstore.New(context.Background(), "sqlite3", "file::memory:?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("Failed to create in-memory store: %v", err)
	}
	defer container.Close()

	deviceStore := container.NewDevice()
	waClient := whatsmeow.NewClient(deviceStore, waLog.Noop)
	client := &Client{
		Client: waClient,
		logger: waLog.Noop,
	}

	exitCode := -1
	client.HandleConnectionEvent(context.Background(), &events.ClientOutdated{}, func(code int) {
		exitCode = code
	})

	if exitCode != 1 {
		t.Errorf("Expected exitCode 1 on ClientOutdated, got %d", exitCode)
	}
}

func TestHandleConnectionEvent_ClientOutdated_WithID(t *testing.T) {
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

	if err := deviceStore.Save(context.Background()); err != nil {
		t.Fatalf("Failed to save device: %v", err)
	}

	waClient := whatsmeow.NewClient(deviceStore, waLog.Noop)
	client := &Client{
		Client: waClient,
		logger: waLog.Noop,
	}

	exitCode := -1
	client.HandleConnectionEvent(context.Background(), &events.ClientOutdated{}, func(code int) {
		exitCode = code
	})

	if exitCode != 1 {
		t.Errorf("Expected exitCode 1 on ClientOutdated, got %d", exitCode)
	}
	if deviceStore.ID != nil {
		t.Errorf("Expected session to be deleted and deviceStore.ID to be nil, got: %v", deviceStore.ID)
	}
}

func TestHandleConnectionEvent_LoggedOut_WithID(t *testing.T) {
	container, err := sqlstore.New(context.Background(), "sqlite3", "file::memory:?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("Failed to create in-memory store: %v", err)
	}
	defer container.Close()

	deviceStore := container.NewDevice()
	testJID := types.NewJID("9876543210", types.DefaultUserServer)
	deviceStore.ID = &testJID
	deviceStore.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             make([]byte, 10),
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}

	if err := deviceStore.Save(context.Background()); err != nil {
		t.Fatalf("Failed to save device: %v", err)
	}

	waClient := whatsmeow.NewClient(deviceStore, waLog.Noop)
	client := &Client{
		Client: waClient,
		logger: waLog.Noop,
	}

	exitCode := -1
	client.HandleConnectionEvent(context.Background(), &events.LoggedOut{}, func(code int) {
		exitCode = code
	})

	if exitCode != 1 {
		t.Errorf("Expected exitCode 1 on LoggedOut, got %d", exitCode)
	}
	if deviceStore.ID != nil {
		t.Errorf("Expected session to be deleted and deviceStore.ID to be nil, got: %v", deviceStore.ID)
	}
}

func TestHandleConnectionEvent_ConnectFailure(t *testing.T) {
	client := &Client{
		Client: nil,
		logger: waLog.Noop,
	}

	exitCalled := false
	client.HandleConnectionEvent(context.Background(), &events.ConnectFailure{
		Reason:  events.ConnectFailureReason(405),
		Message: "client outdated",
	}, func(code int) {
		exitCalled = true
	})

	if exitCalled {
		t.Errorf("Did not expect onExit callback to be invoked for generic ConnectFailure event")
	}
}
