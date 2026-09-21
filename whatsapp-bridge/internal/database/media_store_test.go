package database

import (
	"testing"
	"time"

	"whatsapp-bridge/internal/types"
)

func TestMediaStore(t *testing.T) {
	tempDir := t.TempDir()

	ms, err := NewMediaStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create MediaStore: %v", err)
	}
	defer ms.Close()

	// 1. RecordDownload & GetDownload
	now := time.Now().UTC()
	rec := &MediaDownloadRecord{
		MessageID:    "MSG123",
		ChatJID:      "123456@s.whatsapp.net",
		LocalPath:    "media/123456/voice_MSG123.ogg",
		SHA256:       "abcdef0123456789",
		DirectPath:   "/v/t62.7114-24/test.enc",
		DownloadedAt: &now,
		Status:       "ok",
		Error:        "",
	}

	if err := ms.RecordDownload(rec); err != nil {
		t.Fatalf("RecordDownload failed: %v", err)
	}

	fetched, err := ms.GetDownload("MSG123", "123456@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetDownload failed: %v", err)
	}
	if fetched == nil {
		t.Fatal("GetDownload returned nil")
	}
	if fetched.Status != "ok" || fetched.LocalPath != rec.LocalPath || fetched.SHA256 != rec.SHA256 {
		t.Fatalf("Fetched record mismatch: %+v", fetched)
	}

	// 2. Job CRUD
	job := &DownloadJob{
		ID:        "job_test_001",
		Params:    `{"chat":"123456"}`,
		Status:    "running",
		Total:     10,
		StartedAt: now,
	}

	if err := ms.CreateJob(job); err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	active, err := ms.GetActiveJob()
	if err != nil {
		t.Fatalf("GetActiveJob failed: %v", err)
	}
	if active == nil || active.ID != job.ID {
		t.Fatalf("Active job mismatch: %+v", active)
	}

	if err := ms.UpdateJobProgress("job_test_001", 5, 1, 2, "MSG456"); err != nil {
		t.Fatalf("UpdateJobProgress failed: %v", err)
	}

	if err := ms.FinishJob("job_test_001", "completed"); err != nil {
		t.Fatalf("FinishJob failed: %v", err)
	}

	activeAfter, err := ms.GetActiveJob()
	if err != nil {
		t.Fatalf("GetActiveJob after finish failed: %v", err)
	}
	if activeAfter != nil {
		t.Fatalf("Expected no active job after finish, got: %+v", activeAfter)
	}

	finalJob, err := ms.GetJob("job_test_001")
	if err != nil || finalJob == nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if finalJob.Status != "completed" || finalJob.Done != 5 || finalJob.Failed != 1 || finalJob.Skipped != 2 {
		t.Fatalf("Final job state mismatch: %+v", finalJob)
	}
}

func TestListAudioMessagesAndGetMediaInfo(t *testing.T) {
	tempDir := t.TempDir()

	store, err := NewMessageStoreWithDir(tempDir)
	if err != nil {
		t.Fatalf("Failed to create MessageStore: %v", err)
	}
	defer store.Close()

	// Insert test chats and messages
	now := time.Now().UTC()
	err = store.StoreChat("chat1@s.whatsapp.net", "Chat One", now)
	if err != nil {
		t.Fatalf("StoreChat failed: %v", err)
	}

	mediaKey := make([]byte, 32)
	copy(mediaKey, "01234567890123456789012345678901")
	sha := make([]byte, 32)
	encSha := make([]byte, 32)

	err = store.StoreMessage("M1", "chat1@s.whatsapp.net", "972500000001", "", now, true,
		types.MediaMetadata{MediaType: "audio", Filename: "audio_01.ogg", URL: "https://mmg.whatsapp.net/v/t62/test1.enc",
			MediaKey: mediaKey, FileSHA256: sha, FileEncSHA256: encSha, FileLength: 1024})
	if err != nil {
		t.Fatalf("StoreMessage M1 failed: %v", err)
	}

	err = store.StoreMessage("M2", "chat1@s.whatsapp.net", "972500000001", "hello", now.Add(time.Second), false,
		types.MediaMetadata{})
	if err != nil {
		t.Fatalf("StoreMessage M2 failed: %v", err)
	}

	// 1. Test GetMediaInfo
	info, err := store.GetMediaInfo("M1", "chat1@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetMediaInfo failed: %v", err)
	}
	if info == nil {
		t.Fatal("Expected M1 info, got nil")
	}
	if info.MediaType != "audio" || info.FileLength != 1024 || !info.IsFromMe {
		t.Fatalf("MediaInfo fields mismatch: %+v", info)
	}

	// Non-media message returns empty mediaType or nil
	nonMedia, err := store.GetMediaInfo("M2", "chat1@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetMediaInfo for M2 failed: %v", err)
	}
	if nonMedia == nil || nonMedia.MediaType != "" {
		t.Fatalf("Expected empty mediaType for M2, got: %+v", nonMedia)
	}

	// 2. Test ListAudioMessages
	list, err := store.ListAudioMessages([]string{"chat1@s.whatsapp.net"}, "", "", 10, false)
	if err != nil {
		t.Fatalf("ListAudioMessages failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Expected 1 audio message, got %d", len(list))
	}
	if list[0].ID != "M1" {
		t.Fatalf("Expected M1, got %s", list[0].ID)
	}
}

func TestJobInterruptAndCancel(t *testing.T) {
	ms, err := NewMediaStore(t.TempDir())
	if err != nil {
		t.Fatalf("Failed to create MediaStore: %v", err)
	}
	defer ms.Close()

	for _, id := range []string{"stuck", "done"} {
		if err := ms.CreateJob(&DownloadJob{ID: id, Status: "running", StartedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ms.FinishJob("done", "completed"); err != nil {
		t.Fatal(err)
	}

	// Startup cleanup: only the job left running changes, and nothing blocks new batches
	n, err := ms.MarkInterruptedJobs()
	if err != nil || n != 1 {
		t.Fatalf("MarkInterruptedJobs = (%d, %v), want (1, nil)", n, err)
	}
	if job, _ := ms.GetJob("stuck"); job.Status != "interrupted" || job.FinishedAt == nil {
		t.Errorf("stuck job = %+v, want interrupted with finished_at", job)
	}
	if active, _ := ms.GetActiveJob(); active != nil {
		t.Errorf("GetActiveJob = %+v, want nil", active)
	}

	// Cancel only affects running jobs
	if ok, err := ms.CancelJob("done"); ok || err != nil {
		t.Errorf("CancelJob(completed) = (%v, %v), want (false, nil)", ok, err)
	}
	if job, _ := ms.GetJob("done"); job.Status != "completed" {
		t.Errorf("completed job status changed to %q", job.Status)
	}
	if ok, _ := ms.CancelJob("missing"); ok {
		t.Error("CancelJob(missing) = true, want false")
	}
	if err := ms.CreateJob(&DownloadJob{ID: "live", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if ok, err := ms.CancelJob("live"); !ok || err != nil {
		t.Errorf("CancelJob(running) = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestIsPermanentDownloadFailure(t *testing.T) {
	permanent := map[string]bool{
		DownloadStatusOK:                 false,
		DownloadStatusFailed:             false,
		DownloadStatusRetryTimeout:       false,
		DownloadStatusNotOnPhone:         true,
		DownloadStatusRekeyedOnPhone:     true,
		DownloadStatusIncompleteMetadata: true,
	}
	for status, want := range permanent {
		if got := IsPermanentDownloadFailure(status); got != want {
			t.Errorf("IsPermanentDownloadFailure(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestListAudioMessagesTimeFilter(t *testing.T) {
	store, err := NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("Failed to create MessageStore: %v", err)
	}
	defer store.Close()

	if err := store.StoreChat("c@s.whatsapp.net", "C", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i, ts := range []string{"2026-01-01T09:00:00Z", "2026-01-01T15:00:00Z", "2026-01-02T09:00:00Z"} {
		at, _ := time.Parse(time.RFC3339, ts)
		id := string(rune('A' + i))
		if err := store.StoreMessage(id, "c@s.whatsapp.net", "1", "", at, false,
			types.MediaMetadata{MediaType: "audio", Filename: "a.ogg", URL: "https://mmg.whatsapp.net/v/x.enc",
				MediaKey: key, FileSHA256: key, FileEncSHA256: key, FileLength: 1}); err != nil {
			t.Fatal(err)
		}
	}

	// Same instant written three ways must select the same rows. Before normalization,
	// "2026-01-01T12:00:00Z" compared as text sorted after every stored "2026-01-01 ..." value.
	for _, after := range []string{"2026-01-01T12:00:00Z", "2026-01-01 12:00:00", "2026-01-01T14:00:00+02:00"} {
		list, err := store.ListAudioMessages([]string{"c@s.whatsapp.net"}, after, "", 0, false)
		if err != nil {
			t.Fatalf("after=%q: %v", after, err)
		}
		if len(list) != 2 || list[0].ID != "C" || list[1].ID != "B" {
			t.Errorf("after=%q: got %d rows %+v, want C, B", after, len(list), list)
		}
	}

	list, err := store.ListAudioMessages([]string{"c@s.whatsapp.net"}, "", "2026-01-02", 0, false)
	if err != nil || len(list) != 2 {
		t.Errorf("before=2026-01-02: got %d rows, err %v; want 2", len(list), err)
	}

	if _, err := store.ListAudioMessages([]string{"c@s.whatsapp.net"}, "yesterday", "", 0, false); err == nil {
		t.Error("invalid after: expected error")
	}
}

func TestNormalizeTimeFilter(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"2026-01-01":                "2026-01-01 00:00:00+00:00",
		"2026-01-01T12:30:00Z":      "2026-01-01 12:30:00+00:00",
		"2026-01-01T14:30:00+02:00": "2026-01-01 12:30:00+00:00",
		"2026-01-01 12:30:00":       "2026-01-01 12:30:00+00:00",
		"2026-01-01 12:30:00+00:00": "2026-01-01 12:30:00+00:00",
	}
	for in, want := range cases {
		got, err := NormalizeTimeFilter(in)
		if err != nil || got != want {
			t.Errorf("NormalizeTimeFilter(%q) = (%q, %v), want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeTimeFilter("31/12/2026"); err == nil {
		t.Error("expected error for unsupported format")
	}
}
