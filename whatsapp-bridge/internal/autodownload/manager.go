// Package autodownload captures media as it arrives, for the listeners that ask for it.
//
// A listener is an ordinary webhook config with auto_download on (schema v2): its triggers
// decide which messages match, exactly as they do for delivery. This package owns only what
// happens after a match — a bounded queue and a small worker pool, so that the whatsmeow
// event handler never waits for a download (AUDIO_DOWNLOAD_PLAN.md §4.5).
package autodownload

import (
	"context"
	"sync"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// Downloader is the one thing this package needs from the WhatsApp client. Keeping it this
// narrow is what lets the manager be tested without a client, a store or a network.
type Downloader interface {
	// DownloadVoice fetches one already-stored message's media and returns where it landed.
	DownloadVoice(ctx context.Context, messageID, chatJID string) (path string, cached bool, err error)
}

// Job is one matched message waiting to be downloaded. The payload is deliberately just the
// identity of the message: the worker re-reads the decryption material from messages.db, so
// nothing can go stale between the match and the download.
type Job struct {
	MessageID string
	ChatJID   string
	RuleID    int
	RuleName  string
}

// Options configures the pool. Zero values fall back to the defaults below.
type Options struct {
	Workers   int
	QueueSize int
	Timeout   time.Duration
	// ShutdownGrace is how long Stop lets a download in progress finish before aborting it.
	// It is deliberately shorter than docker-compose's stop_grace_period, so the abort is
	// ours rather than a SIGKILL halfway through writing a file.
	ShutdownGrace time.Duration
}

const (
	defaultWorkers       = 2
	defaultQueueSize     = 100
	defaultTimeout       = 60 * time.Second
	defaultShutdownGrace = 15 * time.Second
)

// Manager holds the queue and its workers.
type Manager struct {
	downloader Downloader
	logger     waLog.Logger
	queue      chan Job
	timeout    time.Duration
	workers    int
	grace      time.Duration

	// quit stops workers from taking new jobs; ctx is what a job actually runs under, so
	// cancelling it aborts a download in progress. Shutdown uses quit first and ctx only
	// if the grace period runs out.
	quit     chan struct{}
	quitOnce sync.Once
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	// inflight keeps one message from being queued twice when several rules match it, or
	// when whatsmeow redelivers an event. Entries are removed once the worker is done.
	mu       sync.Mutex
	inflight map[string]bool
	started  bool
}

// NewManager creates a manager. It does not start any worker; call Start.
func NewManager(downloader Downloader, logger waLog.Logger, opts Options) *Manager {
	if opts.Workers <= 0 {
		opts.Workers = defaultWorkers
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultQueueSize
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.ShutdownGrace <= 0 {
		opts.ShutdownGrace = defaultShutdownGrace
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		downloader: downloader,
		logger:     logger,
		queue:      make(chan Job, opts.QueueSize),
		timeout:    opts.Timeout,
		workers:    opts.Workers,
		grace:      opts.ShutdownGrace,
		quit:       make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		inflight:   make(map[string]bool),
	}
}

// Start launches the worker pool.
func (m *Manager) Start() {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	for i := 1; i <= m.workers; i++ {
		m.wg.Add(1)
		go m.worker(i)
	}
	m.logger.Infof("Auto-download started with %d worker(s), queue size %d", m.workers, cap(m.queue))
}

// Stop shuts the pool down: workers stop taking new jobs and the one each is running is
// given ShutdownGrace to finish, because a voice note that is already downloading is
// usually seconds from being safe on disk. Anything still running after that is aborted,
// and anything still queued is dropped — those messages are in messages.db and can be
// fetched later, whereas a half-written file cannot be. Safe to call more than once, and
// on a manager that was never started.
func (m *Manager) Stop() {
	m.quitOnce.Do(func() { close(m.quit) })

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(m.grace):
		m.logger.Warnf("Auto-download shutdown grace of %s expired; aborting downloads in progress", m.grace)
		m.cancel()
		<-done
	}
	m.cancel()
}

// Enqueue submits a job. The send is non-blocking: the caller is on the whatsmeow event
// path, so a full queue drops the job with a warning rather than delaying message handling.
// It reports whether the job was accepted.
func (m *Manager) Enqueue(job Job) bool {
	select {
	case <-m.quit:
		return false
	default:
	}

	key := job.ChatJID + "/" + job.MessageID

	m.mu.Lock()
	if m.inflight[key] {
		m.mu.Unlock()
		return false
	}
	m.inflight[key] = true
	m.mu.Unlock()

	select {
	case m.queue <- job:
		return true
	default:
		m.release(key)
		m.logger.Warnf("Auto-download queue full (%d), dropped message %s in %s; it can still be fetched with POST /api/download",
			cap(m.queue), job.MessageID, job.ChatJID)
		return false
	}
}

func (m *Manager) release(key string) {
	m.mu.Lock()
	delete(m.inflight, key)
	m.mu.Unlock()
}

// QueueDepth returns the number of jobs waiting. Used by tests and logging.
func (m *Manager) QueueDepth() int {
	return len(m.queue)
}

func (m *Manager) worker(id int) {
	defer m.wg.Done()

	for {
		// Checked on its own first: once shutdown starts, a queued job must not be picked
		// up just because select happened to choose that case.
		select {
		case <-m.quit:
			return
		default:
		}

		select {
		case <-m.quit:
			return
		case job := <-m.queue:
			m.run(id, job)
		}
	}
}

func (m *Manager) run(workerID int, job Job) {
	defer m.release(job.ChatJID + "/" + job.MessageID)

	ctx, cancel := context.WithTimeout(m.ctx, m.timeout)
	defer cancel()

	start := time.Now()
	path, cached, err := m.downloader.DownloadVoice(ctx, job.MessageID, job.ChatJID)
	switch {
	case err != nil:
		m.logger.Warnf("Auto-download failed for %s in %s (rule %d %q, worker %d, %s): %v",
			job.MessageID, job.ChatJID, job.RuleID, job.RuleName, workerID, time.Since(start).Round(time.Millisecond), err)
	case cached:
		m.logger.Infof("Auto-download: %s in %s already on disk at %s (rule %d %q)",
			job.MessageID, job.ChatJID, path, job.RuleID, job.RuleName)
	default:
		m.logger.Infof("Auto-downloaded %s in %s to %s in %s (rule %d %q, worker %d)",
			job.MessageID, job.ChatJID, path, time.Since(start).Round(time.Millisecond), job.RuleID, job.RuleName, workerID)
	}
}
