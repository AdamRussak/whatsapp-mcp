package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Download statuses stored in media_downloads.status
const (
	DownloadStatusOK                 = "ok"
	DownloadStatusFailed             = "failed"               // transient; retried on the next run
	DownloadStatusRetryTimeout       = "retry_timeout"        // phone didn't answer the media retry in time; retried
	DownloadStatusNotOnPhone         = "expired_not_on_phone" // CDN copy expired and the phone no longer has the file
	DownloadStatusRekeyedOnPhone     = "rekeyed_on_phone"     // phone re-uploaded with a media key we don't have
	DownloadStatusIncompleteMetadata = "incomplete_metadata"  // messages.db lacks key, hashes or URL
)

// IsPermanentDownloadFailure reports whether a status can't change by retrying,
// so batch re-runs skip it unless retry_failed is set.
func IsPermanentDownloadFailure(status string) bool {
	switch status {
	case DownloadStatusNotOnPhone, DownloadStatusRekeyedOnPhone, DownloadStatusIncompleteMetadata:
		return true
	}
	return false
}

// MediaDownloadRecord represents a media download result in media.db
type MediaDownloadRecord struct {
	MessageID    string     `json:"message_id"`
	ChatJID      string     `json:"chat_jid"`
	LocalPath    string     `json:"local_path"`
	SHA256       string     `json:"sha256"`
	DirectPath   string     `json:"direct_path"`
	DownloadedAt *time.Time `json:"downloaded_at"`
	Status       string     `json:"status"` // one of the DownloadStatus* constants
	Error        string     `json:"error"`
}

// DownloadJob represents a batch download job in media.db
type DownloadJob struct {
	ID               string     `json:"id"`
	Params           string     `json:"params"`
	Status           string     `json:"status"` // 'running', 'completed', 'failed', 'cancelled'
	Total            int        `json:"total"`
	Done             int        `json:"done"`
	Failed           int        `json:"failed"`
	Skipped          int        `json:"skipped"`
	CurrentMessageID string     `json:"current_message_id"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

// MediaStore handles operations on store/media.db
type MediaStore struct {
	db *sql.DB
}

// NewMediaStore initializes a MediaStore connected to media.db
func NewMediaStore(storeDir string) (*MediaStore, error) {
	if storeDir == "" {
		storeDir = "store"
	}
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %w", err)
	}

	dbPath := filepath.Join(storeDir, "media.db")
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_busy_timeout=5000", dbPath)

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open media database: %w", err)
	}

	db.SetMaxOpenConns(1)

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS media_downloads (
			message_id TEXT,
			chat_jid TEXT,
			local_path TEXT,
			sha256 TEXT,
			direct_path TEXT,
			downloaded_at TIMESTAMP,
			status TEXT,
			error TEXT,
			PRIMARY KEY (message_id, chat_jid)
		);

		CREATE TABLE IF NOT EXISTS download_jobs (
			id TEXT PRIMARY KEY,
			params TEXT,
			status TEXT,
			total INTEGER DEFAULT 0,
			done INTEGER DEFAULT 0,
			failed INTEGER DEFAULT 0,
			skipped INTEGER DEFAULT 0,
			current_message_id TEXT,
			started_at TIMESTAMP,
			finished_at TIMESTAMP
		);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create media.db tables: %w", err)
	}

	return &MediaStore{db: db}, nil
}

// Close closes the media database
func (s *MediaStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// RecordDownload stores or updates a media download record
func (s *MediaStore) RecordDownload(rec *MediaDownloadRecord) error {
	var downloadedAt *time.Time
	if rec.DownloadedAt != nil {
		downloadedAt = rec.DownloadedAt
	} else if rec.Status == "ok" {
		now := time.Now().UTC()
		downloadedAt = &now
	}

	_, err := s.db.Exec(`
		INSERT INTO media_downloads (message_id, chat_jid, local_path, sha256, direct_path, downloaded_at, status, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id, chat_jid) DO UPDATE SET
			local_path = excluded.local_path,
			sha256 = excluded.sha256,
			direct_path = excluded.direct_path,
			downloaded_at = excluded.downloaded_at,
			status = excluded.status,
			error = excluded.error
	`, rec.MessageID, rec.ChatJID, rec.LocalPath, rec.SHA256, rec.DirectPath, downloadedAt, rec.Status, rec.Error)
	return err
}

// GetDownload retrieves a media download record by message ID and chat JID
func (s *MediaStore) GetDownload(messageID, chatJID string) (*MediaDownloadRecord, error) {
	row := s.db.QueryRow(`
		SELECT message_id, chat_jid, local_path, sha256, direct_path, downloaded_at, status, error
		FROM media_downloads
		WHERE message_id = ? AND chat_jid = ?
	`, messageID, chatJID)

	var rec MediaDownloadRecord
	var downloadedAt sql.NullTime
	var localPath, sha256, directPath, errStr sql.NullString

	err := row.Scan(&rec.MessageID, &rec.ChatJID, &localPath, &sha256, &directPath, &downloadedAt, &rec.Status, &errStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	rec.LocalPath = localPath.String
	rec.SHA256 = sha256.String
	rec.DirectPath = directPath.String
	rec.Error = errStr.String
	if downloadedAt.Valid {
		rec.DownloadedAt = &downloadedAt.Time
	}

	return &rec, nil
}

// GetDownloadAnyChat retrieves a media download record for messageID regardless of chatJID
func (s *MediaStore) GetDownloadAnyChat(messageID string) (*MediaDownloadRecord, error) {
	row := s.db.QueryRow(`
		SELECT message_id, chat_jid, local_path, sha256, direct_path, downloaded_at, status, error
		FROM media_downloads
		WHERE message_id = ?
		LIMIT 1
	`, messageID)

	var rec MediaDownloadRecord
	var downloadedAt sql.NullTime
	var localPath, sha256, directPath, errStr sql.NullString

	err := row.Scan(&rec.MessageID, &rec.ChatJID, &localPath, &sha256, &directPath, &downloadedAt, &rec.Status, &errStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	rec.LocalPath = localPath.String
	rec.SHA256 = sha256.String
	rec.DirectPath = directPath.String
	rec.Error = errStr.String
	if downloadedAt.Valid {
		rec.DownloadedAt = &downloadedAt.Time
	}

	return &rec, nil
}

// CreateJob creates a new batch download job
func (s *MediaStore) CreateJob(job *DownloadJob) error {
	_, err := s.db.Exec(`
		INSERT INTO download_jobs (id, params, status, total, done, failed, skipped, current_message_id, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, job.ID, job.Params, job.Status, job.Total, job.Done, job.Failed, job.Skipped, job.CurrentMessageID, job.StartedAt)
	return err
}

// UpdateJobProgress updates counts and current message ID for a job
func (s *MediaStore) UpdateJobProgress(id string, done, failed, skipped int, currentMessageID string) error {
	_, err := s.db.Exec(`
		UPDATE download_jobs
		SET done = ?, failed = ?, skipped = ?, current_message_id = ?
		WHERE id = ?
	`, done, failed, skipped, currentMessageID, id)
	return err
}

// FinishJob marks a job as completed, failed, or cancelled
func (s *MediaStore) FinishJob(id string, status string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE download_jobs
		SET status = ?, finished_at = ?
		WHERE id = ?
	`, status, now, id)
	return err
}

// MarkInterruptedJobs marks jobs left 'running' by a previous process as 'interrupted'.
// Call once at startup, before any batch can start; returns the number of jobs changed.
func (s *MediaStore) MarkInterruptedJobs() (int64, error) {
	res, err := s.db.Exec(`
		UPDATE download_jobs
		SET status = 'interrupted', finished_at = ?
		WHERE status = 'running'
	`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CancelJob marks a running job as cancelled. It returns false if no running job has that ID.
func (s *MediaStore) CancelJob(id string) (bool, error) {
	res, err := s.db.Exec(`
		UPDATE download_jobs
		SET status = 'cancelled', finished_at = ?
		WHERE id = ? AND status = 'running'
	`, time.Now().UTC(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// GetJob returns a download job by ID
func (s *MediaStore) GetJob(id string) (*DownloadJob, error) {
	row := s.db.QueryRow(`
		SELECT id, params, status, total, done, failed, skipped, current_message_id, started_at, finished_at
		FROM download_jobs
		WHERE id = ?
	`, id)

	var job DownloadJob
	var currentMsg sql.NullString
	var finishedAt sql.NullTime

	err := row.Scan(&job.ID, &job.Params, &job.Status, &job.Total, &job.Done, &job.Failed, &job.Skipped, &currentMsg, &job.StartedAt, &finishedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	job.CurrentMessageID = currentMsg.String
	if finishedAt.Valid {
		job.FinishedAt = &finishedAt.Time
	}

	return &job, nil
}

// GetActiveJob returns any job currently in 'running' state
func (s *MediaStore) GetActiveJob() (*DownloadJob, error) {
	row := s.db.QueryRow(`
		SELECT id, params, status, total, done, failed, skipped, current_message_id, started_at, finished_at
		FROM download_jobs
		WHERE status = 'running'
		LIMIT 1
	`)

	var job DownloadJob
	var currentMsg sql.NullString
	var finishedAt sql.NullTime

	err := row.Scan(&job.ID, &job.Params, &job.Status, &job.Total, &job.Done, &job.Failed, &job.Skipped, &currentMsg, &job.StartedAt, &finishedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	job.CurrentMessageID = currentMsg.String
	if finishedAt.Valid {
		job.FinishedAt = &finishedAt.Time
	}

	return &job, nil
}
