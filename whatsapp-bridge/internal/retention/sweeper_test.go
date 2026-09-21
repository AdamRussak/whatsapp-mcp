package retention

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/types"

	waLog "go.mau.fi/whatsmeow/util/log"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func daysAgo(d int) time.Time { return now.Add(-time.Duration(d) * 24 * time.Hour) }

type fixture struct {
	t        *testing.T
	dir      string
	messages *database.MessageStore
	media    *database.MediaStore
}

// newFixture builds a store with a fake whatsapp.db and messages.db beside media/, so every
// test can prove the files retention must never touch are still there afterwards.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	messages, err := database.NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { messages.Close() })
	media, err := database.NewMediaStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })
	if err := os.WriteFile(filepath.Join(dir, "whatsapp.db"), []byte("the session"), 0644); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, dir: dir, messages: messages, media: media}
}

func (f *fixture) listener(name string, enabled bool, retentionDays int) int {
	f.t.Helper()
	cfg := &types.WebhookConfig{Name: name, Enabled: enabled, AutoDownload: true, RetentionDays: retentionDays}
	if err := f.messages.StoreWebhookConfig(cfg); err != nil {
		f.t.Fatal(err)
	}
	return cfg.ID
}

// file writes a downloaded file under media/ and its ok record, returning its local path.
func (f *fixture) file(id string) string {
	f.t.Helper()
	rel := filepath.Join("media", "972547669192", id+".ogg")
	abs := filepath.Join(f.dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("OggS "+id), 0644); err != nil {
		f.t.Fatal(err)
	}
	f.record(id, rel)
	return rel
}

func (f *fixture) record(id, localPath string) {
	f.t.Helper()
	if err := f.media.RecordDownload(&database.MediaDownloadRecord{
		MessageID: id, ChatJID: "c@lid", Status: database.DownloadStatusOK, LocalPath: localPath,
	}); err != nil {
		f.t.Fatal(err)
	}
}

// capture logs a successful auto-download by a listener at a given time.
func (f *fixture) capture(ruleID int, id string, at time.Time) {
	f.t.Helper()
	if _, err := f.messages.GetDB().Exec(`INSERT INTO webhook_logs
		(webhook_config_id, message_id, chat_jid, response_body, attempt_count, delivered_at, created_at, event_type)
		VALUES (?, ?, 'c@lid', 'x', 1, ?, ?, 'auto_download')`,
		ruleID, id, at.Format("2006-01-02 15:04:05"), at.Format("2006-01-02 15:04:05")); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) sweeper(opts Options) *Sweeper {
	s := New(f.messages, f.media, f.dir, waLog.Noop, opts)
	s.now = func() time.Time { return now }
	return s
}

func (f *fixture) sweep(opts Options) Result {
	f.t.Helper()
	r, err := f.sweeper(opts).Sweep()
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(f.dir, rel))
	return err == nil
}

func (f *fixture) status(id string) string {
	f.t.Helper()
	rec, err := f.media.GetDownload(id, "c@lid")
	if err != nil || rec == nil {
		f.t.Fatalf("no record for %s: %v", id, err)
	}
	return rec.Status
}

func (f *fixture) retentionLogs() int {
	f.t.Helper()
	var n int
	if err := f.messages.GetDB().QueryRow(`SELECT COUNT(*) FROM webhook_logs WHERE event_type = ?`, EventType).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// Every test ends here: nothing outside media/ may ever be touched.
func (f *fixture) sessionIntact() {
	f.t.Helper()
	body, err := os.ReadFile(filepath.Join(f.dir, "whatsapp.db"))
	if err != nil || string(body) != "the session" {
		f.t.Fatalf("whatsapp.db was touched: %q, %v", body, err)
	}
}

func TestExpiredCaptureIsRemovedRecordedAndLogged(t *testing.T) {
	f := newFixture(t)
	rule := f.listener("Voice notes", true, 30)
	path := f.file("OLD")
	f.capture(rule, "OLD", daysAgo(31))

	r := f.sweep(Options{})
	if r.Due != 1 || r.Removed != 1 {
		t.Fatalf("result = %+v, want 1 due and 1 removed", r)
	}
	if f.exists(path) {
		t.Fatal("the expired file is still on disk")
	}
	if f.status("OLD") != database.DownloadStatusPruned {
		t.Fatalf("status = %s, want pruned", f.status("OLD"))
	}
	if f.retentionLogs() != 1 {
		t.Fatal("the removal was not logged against the listener")
	}
	f.sessionIntact()

	// A second sweep finds nothing left to do: pruned files are not candidates.
	if r := f.sweep(Options{}); r.Due != 0 {
		t.Fatalf("second sweep: %+v, want nothing due", r)
	}
}

func TestKeptWhenAnyCaptureStillWantsIt(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fixture)
	}{
		{"not yet expired", func(f *fixture) {
			f.capture(f.listener("30d", true, 30), "F", daysAgo(29))
		}},
		{"another listener keeps forever", func(f *fixture) {
			f.capture(f.listener("30d", true, 30), "F", daysAgo(90))
			f.capture(f.listener("forever", true, 0), "F", daysAgo(90))
		}},
		{"another listener has not expired yet", func(f *fixture) {
			f.capture(f.listener("7d", true, 7), "F", daysAgo(10))
			f.capture(f.listener("30d", true, 30), "F", daysAgo(10))
		}},
		{"the listener is disabled", func(f *fixture) {
			f.capture(f.listener("off", false, 1), "F", daysAgo(90))
		}},
		{"a re-capture resets the clock", func(f *fixture) {
			rule := f.listener("30d", true, 30)
			f.capture(rule, "F", daysAgo(90))
			f.capture(rule, "F", daysAgo(5))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			path := f.file("F")
			tc.setup(f)

			if r := f.sweep(Options{}); r.Due != 0 || r.Removed != 0 {
				t.Fatalf("result = %+v, want the file kept", r)
			}
			if !f.exists(path) || f.status("F") != database.DownloadStatusOK {
				t.Fatal("a file that should have been kept was removed")
			}
			f.sessionIntact()
		})
	}
}

// A file fetched by hand has no capture log. However aggressive a listener's retention,
// it is never retention's to remove.
func TestManualDownloadIsNeverTouched(t *testing.T) {
	f := newFixture(t)
	f.listener("aggressive", true, 1)
	path := f.file("MANUAL")

	if r := f.sweep(Options{}); r.Due != 0 {
		t.Fatalf("result = %+v, want a manual download left alone", r)
	}
	if !f.exists(path) {
		t.Fatal("a manually downloaded file was removed")
	}
	f.sessionIntact()
}

// Every way a stored path could point outside media/ — or at something other than a voice
// note inside it — is refused, and the session database survives all of them.
func TestUnsafePathsAreRefused(t *testing.T) {
	tests := []struct {
		name      string
		localPath string
		prepare   func(f *fixture) // plants whatever the path points at
	}{
		{"not under media/", "whatsapp.db", nil},
		{"traversal out of media/", "media/../whatsapp.db", nil},
		{"absolute path", "/etc/passwd", nil},
		{"index.json inside media/", "media/972547669192/index.json", func(f *fixture) {
			os.MkdirAll(filepath.Join(f.dir, "media", "972547669192"), 0755)
			os.WriteFile(filepath.Join(f.dir, "media", "972547669192", "index.json"), []byte("{}"), 0644)
		}},
		{"a symlink disguised as audio, pointing at the session", "media/972547669192/evil.ogg", func(f *fixture) {
			os.MkdirAll(filepath.Join(f.dir, "media", "972547669192"), 0755)
			os.Symlink(filepath.Join(f.dir, "whatsapp.db"), filepath.Join(f.dir, "media", "972547669192", "evil.ogg"))
		}},
		{"a symlink to another voice note inside media/", "media/972547669192/alias.ogg", func(f *fixture) {
			f.file("REAL")
			os.Symlink(filepath.Join(f.dir, "media", "972547669192", "REAL.ogg"), filepath.Join(f.dir, "media", "972547669192", "alias.ogg"))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.prepare != nil {
				tc.prepare(f)
			}
			rule := f.listener("aggressive", true, 1)
			f.record("BAD", tc.localPath)
			f.capture(rule, "BAD", daysAgo(90))

			r := f.sweep(Options{})
			if r.Removed != 0 {
				t.Fatalf("result = %+v: an unsafe path was removed", r)
			}
			if f.status("BAD") == database.DownloadStatusPruned && r.Gone == 0 {
				t.Fatal("an unsafe record was marked pruned")
			}
			f.sessionIntact()
			if tc.name == "index.json inside media/" && !f.exists("media/972547669192/index.json") {
				t.Fatal("index.json was removed")
			}
			if tc.name == "a symlink to another voice note inside media/" && !f.exists("media/972547669192/REAL.ogg") {
				t.Fatal("deleting through a symlink removed the file it points at")
			}
		})
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	rule := f.listener("Voice notes", true, 30)
	path := f.file("OLD")
	f.capture(rule, "OLD", daysAgo(31))

	r := f.sweep(Options{DryRun: true})
	if !r.DryRun || r.Due != 1 || r.Removed != 1 {
		t.Fatalf("result = %+v, want the dry run to report 1 removal", r)
	}
	if !f.exists(path) || f.status("OLD") != database.DownloadStatusOK || f.retentionLogs() != 0 {
		t.Fatal("a dry run changed the disk, the record or the log")
	}
}

// One misconfigured listener must not be able to empty the archive in a single pass.
func TestCapLimitsOneSweepOldestFirst(t *testing.T) {
	f := newFixture(t)
	rule := f.listener("1 day", true, 1)
	ages := map[string]int{"A": 50, "B": 40, "C": 30, "D": 20, "E": 10}
	for id, age := range ages {
		f.file(id)
		f.capture(rule, id, daysAgo(age))
	}

	r := f.sweep(Options{MaxPerSweep: 2})
	if r.Due != 5 || r.Removed != 2 || !r.Capped {
		t.Fatalf("result = %+v, want 5 due, 2 removed, capped", r)
	}
	for id, wantGone := range map[string]bool{"A": true, "B": true, "C": false, "D": false, "E": false} {
		if gone := !f.exists("media/972547669192/" + id + ".ogg"); gone != wantGone {
			t.Errorf("%s removed = %v, want %v (oldest expiry first)", id, gone, wantGone)
		}
	}

	// The rest go on later sweeps.
	f.sweep(Options{MaxPerSweep: 2})
	f.sweep(Options{MaxPerSweep: 2})
	if r := f.sweep(Options{MaxPerSweep: 2}); r.Due != 0 {
		t.Fatalf("after four sweeps: %+v, want nothing left", r)
	}
}

// A file already missing from disk is still due; it is recorded as pruned with no file
// operation, so the record stops claiming a download that is not there.
func TestAlreadyMissingFileIsMarkedPruned(t *testing.T) {
	f := newFixture(t)
	rule := f.listener("30d", true, 30)
	path := f.file("GONE")
	os.Remove(filepath.Join(f.dir, path))
	f.capture(rule, "GONE", daysAgo(31))

	r := f.sweep(Options{})
	if r.Gone != 1 || r.Removed != 0 {
		t.Fatalf("result = %+v, want 1 already gone", r)
	}
	if f.status("GONE") != database.DownloadStatusPruned {
		t.Fatal("a missing file's record was not marked pruned")
	}
	f.sessionIntact()
}

func TestPlanIsReadOnly(t *testing.T) {
	f := newFixture(t)
	rule := f.listener("30d", true, 30)
	path := f.file("OLD")
	f.capture(rule, "OLD", daysAgo(31))

	due, err := f.sweeper(Options{}).Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].MessageID != "OLD" || due[0].RetentionDays != 30 || due[0].LocalPath != path {
		t.Fatalf("plan = %+v", due)
	}
	if !due[0].ExpiredAt.Equal(daysAgo(1)) {
		t.Fatalf("expired_at = %s, want capture time + 30 days", due[0].ExpiredAt)
	}
	if !f.exists(path) || f.status("OLD") != database.DownloadStatusOK {
		t.Fatal("Plan changed something")
	}
}

// A capture attributed to a listener that no longer exists keeps the file. The database
// cannot produce this today — webhook_logs has a foreign key to its listener, and deleting
// a listener deletes its logs — so it is tested on the decision itself: if that ever
// changes, an orphaned capture must fail closed, not count as "expired".
func TestOrphanedCaptureKeepsTheFile(t *testing.T) {
	rules := map[int]*types.WebhookConfig{
		1: {ID: 1, Name: "30d", Enabled: true, RetentionDays: 30},
	}
	captures := []database.CaptureRecord{
		{RuleID: 1, MessageID: "F", ChatJID: "c@lid", CapturedAt: daysAgo(90)},
		{RuleID: 9999, MessageID: "F", ChatJID: "c@lid", CapturedAt: daysAgo(90)},
	}
	if _, due := expired(captures, rules, now); due {
		t.Fatal("a capture by a missing listener was treated as expired")
	}
	if _, due := expired(nil, rules, now); due {
		t.Fatal("a file with no captures at all was treated as due")
	}
}
