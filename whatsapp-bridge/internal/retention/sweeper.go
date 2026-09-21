// Package retention removes captured media once every listener that captured it has kept it
// for as long as it asked to.
//
// This is the only code in the bridge that deletes user data, so the rules are written to
// fail closed — when anything is unclear, the file is kept:
//
//   - A file is only a candidate if at least one listener captured it (a successful
//     auto_download row in webhook_logs). A file fetched by hand — POST /api/download or a
//     batch — has no such row and is never touched.
//   - It is removed only when EVERY capture of it belongs to an enabled listener with a
//     retention period that has run out. One capture by a listener that keeps forever, is
//     disabled, or has not yet expired keeps the file. So does a re-capture, which resets
//     that listener's clock.
//   - The path goes through mediapath.Resolve — the same gate /api/media/ serves through —
//     and must additionally be a regular file, not a symlink. Only .ogg and .mp3 under
//     store/media/ can ever be removed; never index.json, never a database.
//   - At most MaxPerSweep files per sweep, oldest expiry first, so one misconfigured
//     listener cannot empty the archive in a single pass.
//   - Every removal marks the media.db record pruned (so nothing downloads it again) and
//     writes a "retention" row to the listener's log, where the UI shows it.
//   - DryRun reports what would go and changes nothing.
package retention

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/mediapath"
	"whatsapp-bridge/internal/types"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// EventType is what a removal is logged as in webhook_logs.
const EventType = "retention"

// Options configures the sweeper. Zero values fall back to the defaults.
type Options struct {
	Interval    time.Duration // between sweeps
	FirstDelay  time.Duration // after Start, before the first sweep
	MaxPerSweep int           // removal cap per sweep
	DryRun      bool          // report only; delete nothing, record nothing
	// Now replaces the clock, so a rehearsal can ask what a sweep would do on a later date.
	Now func() time.Time
}

const (
	defaultInterval    = time.Hour
	defaultFirstDelay  = time.Minute
	defaultMaxPerSweep = 500
)

// Candidate is a file due for removal.
type Candidate struct {
	MessageID     string    `json:"message_id"`
	ChatJID       string    `json:"chat_jid"`
	LocalPath     string    `json:"local_path"`
	RuleID        int       `json:"rule_id"`
	RuleName      string    `json:"rule_name"`
	RetentionDays int       `json:"retention_days"`
	CapturedAt    time.Time `json:"captured_at"`
	ExpiredAt     time.Time `json:"expired_at"`
}

// Result summarises one sweep.
type Result struct {
	At      time.Time `json:"at"`
	DryRun  bool      `json:"dry_run"`
	Due     int       `json:"due"`     // files past their retention
	Removed int       `json:"removed"` // deleted (or, in a dry run, would have been)
	Gone    int       `json:"gone"`    // already missing from disk; marked pruned
	Refused int       `json:"refused"` // failed the path checks; kept and logged
	Failed  int       `json:"failed"`  // an error on the way; kept
	Capped  bool      `json:"capped"`  // more were due than MaxPerSweep allows
}

// Sweeper runs retention on a schedule.
type Sweeper struct {
	messages *database.MessageStore
	media    *database.MediaStore
	storeDir string
	logger   waLog.Logger
	opts     Options
	now      func() time.Time

	mu   sync.Mutex
	last *Result

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New creates a sweeper. It does nothing until Start or Sweep is called.
func New(messages *database.MessageStore, media *database.MediaStore, storeDir string, logger waLog.Logger, opts Options) *Sweeper {
	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	}
	if opts.FirstDelay <= 0 {
		opts.FirstDelay = defaultFirstDelay
	}
	if opts.MaxPerSweep <= 0 {
		opts.MaxPerSweep = defaultMaxPerSweep
	}
	if storeDir == "" {
		storeDir = "store"
	}
	clock := opts.Now
	if clock == nil {
		clock = time.Now
	}
	return &Sweeper{
		messages: messages,
		media:    media,
		storeDir: storeDir,
		logger:   logger,
		opts:     opts,
		now:      clock,
		stop:     make(chan struct{}),
	}
}

// DryRun reports whether this sweeper only reports.
func (s *Sweeper) DryRun() bool { return s.opts.DryRun }

// Interval is the time between sweeps.
func (s *Sweeper) Interval() time.Duration { return s.opts.Interval }

// LastResult is the most recent sweep's summary, or nil before the first.
func (s *Sweeper) LastResult() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return nil
	}
	r := *s.last
	return &r
}

// Start runs a sweep after FirstDelay and then every Interval, until Stop.
func (s *Sweeper) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(s.opts.FirstDelay)
		defer timer.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-timer.C:
				if _, err := s.Sweep(); err != nil {
					s.logger.Errorf("Retention sweep failed: %v", err)
				}
				timer.Reset(s.opts.Interval)
			}
		}
	}()
	mode := "removing files"
	if s.opts.DryRun {
		mode = "DRY RUN, nothing is removed"
	}
	s.logger.Infof("Retention started: every %s, at most %d per sweep, %s", s.opts.Interval, s.opts.MaxPerSweep, mode)
}

// Stop ends the schedule and waits for a sweep in progress to finish.
func (s *Sweeper) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
}

// Plan returns every file due for removal now, oldest expiry first. It changes nothing.
func (s *Sweeper) Plan() ([]Candidate, error) {
	configs, err := s.messages.GetAllWebhookConfigs()
	if err != nil {
		return nil, fmt.Errorf("listing listeners: %w", err)
	}
	rules := make(map[int]*types.WebhookConfig, len(configs))
	for _, c := range configs {
		rules[c.ID] = c
	}

	captures, err := s.messages.ListCaptures()
	if err != nil {
		return nil, fmt.Errorf("listing captures: %w", err)
	}
	byFile := make(map[string][]database.CaptureRecord)
	for _, c := range captures {
		key := database.DownloadKey(c.MessageID, c.ChatJID)
		byFile[key] = append(byFile[key], c)
	}

	now := s.now()
	var due []Candidate
	for _, fileCaptures := range byFile {
		candidate, ok := expired(fileCaptures, rules, now)
		if !ok {
			continue
		}

		rec, err := s.media.GetDownload(candidate.MessageID, candidate.ChatJID)
		if err != nil {
			return nil, fmt.Errorf("reading download record: %w", err)
		}
		// Only a file that is actually downloaded is removed; anything else (failed,
		// already pruned, re-keyed) has nothing on disk to delete.
		if rec == nil || rec.Status != database.DownloadStatusOK || rec.LocalPath == "" {
			continue
		}
		candidate.LocalPath = rec.LocalPath
		due = append(due, candidate)
	}

	sort.Slice(due, func(i, j int) bool {
		if !due[i].ExpiredAt.Equal(due[j].ExpiredAt) {
			return due[i].ExpiredAt.Before(due[j].ExpiredAt)
		}
		return due[i].MessageID < due[j].MessageID
	})
	return due, nil
}

// expired decides one file. It is due only if every capture of it has expired under an
// enabled listener with a finite retention; the capture whose expiry is latest is the one
// the removal is attributed to.
func expired(captures []database.CaptureRecord, rules map[int]*types.WebhookConfig, now time.Time) (Candidate, bool) {
	var latest Candidate
	for _, c := range captures {
		rule, ok := rules[c.RuleID]
		if !ok || !rule.Enabled || rule.RetentionDays <= 0 {
			return Candidate{}, false
		}
		expiry := c.CapturedAt.Add(time.Duration(rule.RetentionDays) * 24 * time.Hour)
		if now.Before(expiry) {
			return Candidate{}, false
		}
		if latest.ExpiredAt.IsZero() || expiry.After(latest.ExpiredAt) {
			latest = Candidate{
				MessageID:     c.MessageID,
				ChatJID:       c.ChatJID,
				RuleID:        rule.ID,
				RuleName:      rule.Name,
				RetentionDays: rule.RetentionDays,
				CapturedAt:    c.CapturedAt,
				ExpiredAt:     expiry,
			}
		}
	}
	return latest, len(captures) > 0
}

// Sweep removes what Plan returns, up to MaxPerSweep — or, in a dry run, only reports it.
func (s *Sweeper) Sweep() (Result, error) {
	result := Result{At: s.now().UTC(), DryRun: s.opts.DryRun}
	defer func() {
		s.mu.Lock()
		r := result
		s.last = &r
		s.mu.Unlock()
	}()

	due, err := s.Plan()
	if err != nil {
		return result, err
	}
	result.Due = len(due)
	if len(due) > s.opts.MaxPerSweep {
		due = due[:s.opts.MaxPerSweep]
		result.Capped = true
	}

	for _, c := range due {
		if s.opts.DryRun {
			s.logger.Infof("Retention (dry run): would remove %s (%s, captured %s by %q, kept %d days)",
				c.LocalPath, c.MessageID, c.CapturedAt.Format(time.RFC3339), c.RuleName, c.RetentionDays)
			result.Removed++
			continue
		}
		switch s.remove(c) {
		case outcomeRemoved:
			result.Removed++
		case outcomeGone:
			result.Gone++
		case outcomeRefused:
			result.Refused++
		default:
			result.Failed++
		}
	}

	if result.Due > 0 || result.Refused > 0 {
		s.logger.Infof("Retention sweep: %d due, %d removed, %d already gone, %d refused, %d failed, capped=%t, dry_run=%t",
			result.Due, result.Removed, result.Gone, result.Refused, result.Failed, result.Capped, result.DryRun)
	}
	return result, nil
}

type outcome int

const (
	outcomeFailed outcome = iota
	outcomeRemoved
	outcomeGone
	outcomeRefused
)

// remove deletes one file and records it. Order matters: the file goes first and the record
// second, so a failure between the two leaves a record whose file is missing — which the
// Downloads view shows honestly — rather than a pruned record with a file still on disk.
func (s *Sweeper) remove(c Candidate) outcome {
	mediaRoot := filepath.Join(s.storeDir, "media")

	slashed := filepath.ToSlash(c.LocalPath)
	sub := strings.TrimPrefix(slashed, "media/")
	if sub == slashed {
		// Not under media/ at all: nothing retention may touch.
		s.logger.Warnf("Retention refused %s (%s): path is not under media/", c.LocalPath, c.MessageID)
		return outcomeRefused
	}
	lexical := filepath.Join(mediaRoot, filepath.FromSlash(sub))

	info, err := os.Lstat(lexical)
	if os.IsNotExist(err) {
		// Already gone. It is still due, so record it as pruned, but there is no file
		// operation to make.
		return s.record(c, "already missing from disk", outcomeGone)
	}
	if err != nil {
		s.logger.Warnf("Retention could not inspect %s: %v", c.LocalPath, err)
		return outcomeFailed
	}
	// Stricter than serving: deleting through a symlink could remove the file it points
	// at, so a link is refused outright, wherever it points.
	if !info.Mode().IsRegular() {
		s.logger.Warnf("Retention refused %s (%s): not a regular file", c.LocalPath, c.MessageID)
		return outcomeRefused
	}
	abs, _, ok := mediapath.Resolve(mediaRoot, sub)
	if !ok {
		s.logger.Warnf("Retention refused %s (%s): failed the media path checks", c.LocalPath, c.MessageID)
		return outcomeRefused
	}

	if err := os.Remove(abs); err != nil {
		s.logger.Errorf("Retention could not remove %s: %v", abs, err)
		return outcomeFailed
	}
	s.logger.Infof("Retention removed %s (%s, captured %s by %q, kept %d days)",
		c.LocalPath, c.MessageID, c.CapturedAt.Format(time.RFC3339), c.RuleName, c.RetentionDays)
	return s.record(c, "", outcomeRemoved)
}

// record marks the media.db record pruned and logs the removal against the listener.
func (s *Sweeper) record(c Candidate, note string, success outcome) outcome {
	reason := fmt.Sprintf("removed after %d days by %q", c.RetentionDays, c.RuleName)
	if note != "" {
		reason += "; " + note
	}
	if marked, err := s.media.MarkPruned(c.MessageID, c.ChatJID, reason); err != nil {
		s.logger.Errorf("Retention removed %s but could not mark it pruned: %v", c.LocalPath, err)
		return outcomeFailed
	} else if !marked {
		s.logger.Warnf("Retention removed %s but its record had already changed", c.LocalPath)
	}

	payload, _ := json.Marshal(map[string]any{
		"status":         "pruned",
		"path":           c.LocalPath,
		"captured_at":    c.CapturedAt.UTC().Format(time.RFC3339),
		"retention_days": c.RetentionDays,
		"note":           note,
	})
	now := s.now().UTC()
	if err := s.messages.StoreWebhookLog(&types.WebhookLog{
		WebhookConfigID: c.RuleID,
		MessageID:       c.MessageID,
		ChatJID:         c.ChatJID,
		Payload:         string(payload),
		ResponseBody:    c.LocalPath,
		AttemptCount:    1,
		DeliveredAt:     &now,
		EventType:       EventType,
	}); err != nil {
		s.logger.Errorf("Retention removed %s but could not log it: %v", c.LocalPath, err)
	}
	return success
}
