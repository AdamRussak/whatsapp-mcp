// Command retention rehearses the retention sweep against a copy of the store, without the
// bridge running, and optionally at a simulated date.
//
// It exists for the same reason as cmd/migrate: retention is the only code that deletes
// user data, so its first real deletion on real data should happen on a copy.
//
//	# Read-only: what is due now, and what would be due on a later date.
//	STORE_DIR=../store-test go run ./cmd/retention
//	STORE_DIR=../store-test go run ./cmd/retention -at 2026-11-30
//
//	# Actually remove, on the copy only:
//	touch ../store-test/REHEARSAL
//	STORE_DIR=../store-test go run ./cmd/retention -at 2026-11-30 -apply
//
// -apply refuses to run unless STORE_DIR is set and holds a file named REHEARSAL, so it
// cannot be pointed at the live store by accident. The live bridge runs the same sweep
// itself; there is never a reason to -apply against it.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/retention"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func main() {
	at := flag.String("at", "", "simulate this date (YYYY-MM-DD or RFC 3339) instead of now")
	apply := flag.Bool("apply", false, "remove files, not just list them (needs a REHEARSAL marker)")
	flag.Parse()

	storeDir := os.Getenv("STORE_DIR")
	if storeDir == "" {
		fail("set STORE_DIR to a copy of the store")
	}

	clock := time.Now
	if *at != "" {
		when, ok, err := database.ParseTimeFilter(*at)
		if err != nil || !ok {
			fail("bad -at: %v", err)
		}
		clock = func() time.Time { return when }
	}

	if *apply {
		if _, err := os.Stat(filepath.Join(storeDir, "REHEARSAL")); err != nil {
			fail("-apply needs %s: it only ever runs against a copy", filepath.Join(storeDir, "REHEARSAL"))
		}
	}

	messages, err := database.NewMessageStoreWithDir(storeDir)
	if err != nil {
		fail("opening messages.db: %v", err)
	}
	defer messages.Close()
	media, err := database.NewMediaStore(storeDir)
	if err != nil {
		fail("opening media.db: %v", err)
	}
	defer media.Close()

	sweeper := retention.New(messages, media, storeDir, waLog.Stdout("Retention", "INFO", true),
		retention.Options{DryRun: !*apply, Now: clock})

	due, err := sweeper.Plan()
	if err != nil {
		fail("planning: %v", err)
	}
	fmt.Printf("As of %s: %d file(s) due\n", clock().UTC().Format(time.RFC3339), len(due))
	for _, c := range due {
		fmt.Printf("  %s  %-40s  by %q, kept %d days, captured %s\n",
			c.ExpiredAt.Format("2006-01-02"), c.LocalPath, c.RuleName, c.RetentionDays, c.CapturedAt.Format("2006-01-02 15:04"))
	}

	if !*apply {
		return
	}
	result, err := sweeper.Sweep()
	if err != nil {
		fail("sweeping: %v", err)
	}
	fmt.Printf("Removed %d, already gone %d, refused %d, failed %d, capped %t\n",
		result.Removed, result.Gone, result.Refused, result.Failed, result.Capped)
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "retention: "+format+"\n", args...)
	os.Exit(1)
}
