package autodownload

import (
	"context"
	"sync"
	"testing"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// fakeDownloader records what it was asked for and can be made to block or fail.
type fakeDownloader struct {
	mu      sync.Mutex
	calls   []Job
	done    chan struct{}
	block   chan struct{} // closed to release a blocked call
	err     error
	cached  bool
	ctxErrs []error
}

func newFakeDownloader(expected int) *fakeDownloader {
	return &fakeDownloader{done: make(chan struct{}, expected)}
}

func (f *fakeDownloader) DownloadVoice(ctx context.Context, messageID, chatJID string) (string, bool, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			f.mu.Lock()
			f.ctxErrs = append(f.ctxErrs, ctx.Err())
			f.mu.Unlock()
			f.signal()
			return "", false, ctx.Err()
		}
	}

	f.mu.Lock()
	f.calls = append(f.calls, Job{MessageID: messageID, ChatJID: chatJID})
	f.mu.Unlock()
	f.signal()

	if f.err != nil {
		return "", false, f.err
	}
	return "media/chat/voice_" + messageID + ".ogg", f.cached, nil
}

func (f *fakeDownloader) signal() {
	select {
	case f.done <- struct{}{}:
	default:
	}
}

func (f *fakeDownloader) seen() []Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Job, len(f.calls))
	copy(out, f.calls)
	return out
}

// waitFor blocks until n calls have been signalled, or the test times out.
func (f *fakeDownloader) waitFor(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-f.done:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for download %d of %d", i+1, n)
		}
	}
}

func testLogger() waLog.Logger { return waLog.Noop }

func TestWorkerDownloadsQueuedJob(t *testing.T) {
	fake := newFakeDownloader(1)
	m := NewManager(fake, testLogger(), Options{Workers: 1})
	m.Start()
	defer m.Stop()

	if !m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net", RuleID: 7, RuleName: "voice"}) {
		t.Fatal("Enqueue returned false for an empty queue")
	}
	fake.waitFor(t, 1)

	calls := fake.seen()
	if len(calls) != 1 || calls[0].MessageID != "M1" || calls[0].ChatJID != "c@s.whatsapp.net" {
		t.Fatalf("worker called the downloader with %+v", calls)
	}
}

// A failing download must not take the worker down with it: the next job still runs.
func TestWorkerSurvivesDownloadError(t *testing.T) {
	fake := newFakeDownloader(2)
	fake.err = context.DeadlineExceeded
	m := NewManager(fake, testLogger(), Options{Workers: 1})
	m.Start()
	defer m.Stop()

	m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net"})
	m.Enqueue(Job{MessageID: "M2", ChatJID: "c@s.whatsapp.net"})
	fake.waitFor(t, 2)

	if got := len(fake.seen()); got != 2 {
		t.Fatalf("expected both jobs to run, got %d", got)
	}
}

// §4.5: the caller is on the whatsmeow event path, so a full queue must be refused
// immediately rather than blocking the handler.
func TestEnqueueDoesNotBlockWhenQueueIsFull(t *testing.T) {
	fake := newFakeDownloader(1)
	m := NewManager(fake, testLogger(), Options{Workers: 1, QueueSize: 1})
	// Deliberately not started: nothing drains the queue.

	if !m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net"}) {
		t.Fatal("first Enqueue should fill the single queue slot")
	}

	done := make(chan bool, 1)
	go func() { done <- m.Enqueue(Job{MessageID: "M2", ChatJID: "c@s.whatsapp.net"}) }()

	select {
	case accepted := <-done:
		if accepted {
			t.Fatal("Enqueue accepted a job into a full queue")
		}
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}

	// The refused job must not stay marked in flight, or it could never be queued again.
	if m.inflight["c@s.whatsapp.net/M2"] {
		t.Fatal("a dropped job was left marked in flight")
	}
}

// Several listeners can match one message; it must still be downloaded once.
func TestEnqueueDeduplicatesTheSameMessage(t *testing.T) {
	fake := newFakeDownloader(1)
	m := NewManager(fake, testLogger(), Options{Workers: 1, QueueSize: 10})

	first := m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net", RuleID: 1})
	second := m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net", RuleID: 2})
	if !first || second {
		t.Fatalf("expected the first job to be accepted and the duplicate refused, got %v and %v", first, second)
	}
	if m.QueueDepth() != 1 {
		t.Fatalf("queue depth is %d, expected 1", m.QueueDepth())
	}

	// The same message in a different chat is a different job (a contact's phone and LID
	// JIDs are distinct chats).
	if !m.Enqueue(Job{MessageID: "M1", ChatJID: "1867@lid"}) {
		t.Fatal("the same message ID in another chat was wrongly treated as a duplicate")
	}
}

// Once a download finishes, the message is no longer in flight and can be queued again.
func TestInflightIsReleasedAfterDownload(t *testing.T) {
	fake := newFakeDownloader(2)
	m := NewManager(fake, testLogger(), Options{Workers: 1})
	m.Start()
	defer m.Stop()

	m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net"})
	fake.waitFor(t, 1)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("message stayed marked in flight after its download finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	fake.waitFor(t, 1)
}

// A download that hangs is abandoned at the timeout rather than holding a worker forever.
func TestWorkerAppliesTimeout(t *testing.T) {
	fake := newFakeDownloader(1)
	fake.block = make(chan struct{})
	m := NewManager(fake, testLogger(), Options{Workers: 1, Timeout: 50 * time.Millisecond})
	m.Start()
	defer func() {
		close(fake.block)
		m.Stop()
	}()

	m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net"})
	fake.waitFor(t, 1)

	fake.mu.Lock()
	errs := append([]error(nil), fake.ctxErrs...)
	fake.mu.Unlock()
	if len(errs) != 1 || errs[0] != context.DeadlineExceeded {
		t.Fatalf("expected the download context to expire, got %v", errs)
	}
}

// Stop must return once the workers are done, and must be safe on a manager never started.
func TestStopIsSafe(t *testing.T) {
	m := NewManager(newFakeDownloader(0), testLogger(), Options{})
	m.Stop()

	m2 := NewManager(newFakeDownloader(0), testLogger(), Options{Workers: 3})
	m2.Start()
	done := make(chan struct{})
	go func() {
		m2.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return")
	}
}

func TestDefaultsAreApplied(t *testing.T) {
	m := NewManager(newFakeDownloader(0), testLogger(), Options{})
	if m.workers != defaultWorkers {
		t.Errorf("workers = %d, want %d", m.workers, defaultWorkers)
	}
	if cap(m.queue) != defaultQueueSize {
		t.Errorf("queue size = %d, want %d", cap(m.queue), defaultQueueSize)
	}
	if m.timeout != defaultTimeout {
		t.Errorf("timeout = %s, want %s", m.timeout, defaultTimeout)
	}
}

// A voice note that is already downloading is seconds from being safe on disk, so shutdown
// waits for it rather than leaving a half-written file behind.
func TestStopLetsAnInFlightDownloadFinish(t *testing.T) {
	fake := newFakeDownloader(1)
	fake.block = make(chan struct{})
	m := NewManager(fake, testLogger(), Options{Workers: 1, ShutdownGrace: 2 * time.Second})
	m.Start()

	m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net"})

	// Give the worker time to pick the job up, then start shutting down while it runs.
	time.Sleep(50 * time.Millisecond)
	stopped := make(chan struct{})
	go func() {
		m.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop returned while a download was still in progress")
	case <-time.After(100 * time.Millisecond):
	}

	close(fake.block) // the download completes
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after the download finished")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 {
		t.Fatalf("the in-flight download did not complete: %+v", fake.calls)
	}
	if len(fake.ctxErrs) != 0 {
		t.Fatalf("the download was cancelled rather than allowed to finish: %v", fake.ctxErrs)
	}
}

// A download that will not finish must not hold shutdown open past the grace period.
func TestStopAbortsAfterGracePeriod(t *testing.T) {
	fake := newFakeDownloader(1)
	fake.block = make(chan struct{})
	defer close(fake.block)

	m := NewManager(fake, testLogger(), Options{Workers: 1, ShutdownGrace: 100 * time.Millisecond})
	m.Start()
	m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net"})
	time.Sleep(50 * time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		m.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung on a download that never finished")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.ctxErrs) != 1 || fake.ctxErrs[0] != context.Canceled {
		t.Fatalf("expected the stuck download to be cancelled, got %v", fake.ctxErrs)
	}
}

// Once shutdown has begun, nothing new is accepted.
func TestEnqueueAfterStopIsRefused(t *testing.T) {
	m := NewManager(newFakeDownloader(0), testLogger(), Options{Workers: 1})
	m.Start()
	m.Stop()

	if m.Enqueue(Job{MessageID: "M1", ChatJID: "c@s.whatsapp.net"}) {
		t.Fatal("Enqueue accepted a job after Stop")
	}
}
