// Command migrate applies any pending messages.db migrations and reports the resulting
// schema version, without connecting to WhatsApp.
//
// It exists for §4.6 step 4 of AUDIO_DOWNLOAD_PLAN.md: rehearse a migration on a copy
// before it reaches the live database.
//
//	sudo sqlite3 "file:store/messages.db?mode=ro" ".backup store-test/messages.db"
//	STORE_DIR=../store-test go run ./cmd/migrate
//
// The bridge applies the same migrations itself at startup, so this is only for
// rehearsals and for inspecting a copy.
package main

import (
	"fmt"
	"os"

	"whatsapp-bridge/internal/database"
)

func main() {
	dir := os.Getenv("STORE_DIR")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "STORE_DIR is required (point it at a copy, never the live store while the bridge runs)")
		os.Exit(2)
	}

	store, err := database.NewMessageStoreWithDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	version, err := store.SchemaVersion()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not read schema version: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s/messages.db is at schema version %d\n", dir, version)
}
