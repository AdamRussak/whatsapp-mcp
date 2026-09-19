package database

import (
	"testing"
	"time"
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
		"audio", "audio_01.ogg", "https://mmg.whatsapp.net/v/t62/test1.enc", mediaKey, sha, encSha, 1024)
	if err != nil {
		t.Fatalf("StoreMessage M1 failed: %v", err)
	}

	err = store.StoreMessage("M2", "chat1@s.whatsapp.net", "972500000001", "hello", now.Add(time.Second), false,
		"", "", "", nil, nil, nil, 0)
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
	list, err := store.ListAudioMessages([]string{"chat1@s.whatsapp.net"}, "", "", 10)
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
