package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds application configuration
type Config struct {
	APIPort            int
	StoreDir           string
	DownloadPaceMs     int
	MediaRetryTimeoutS int
	PublicBaseURL      string

	// Auto-download knobs. These are operational only — *who* gets captured is defined by
	// webhook configs in messages.db, not here, so it can change without a restart.
	AutoDownloadEnabled   bool
	AutoDownloadWorkers   int
	AutoDownloadQueueSize int
	AutoDownloadTimeoutS  int

	// Retention: removes a listener's captures once its retention_days have passed. Which
	// listeners keep what is set per listener in the UI; these are operational only.
	RetentionEnabled         bool
	RetentionDryRun          bool
	RetentionIntervalMinutes int
	RetentionMaxPerSweep     int
}

// NewConfig creates a new configuration with default values and environment overrides
func NewConfig() *Config {
	cfg := &Config{
		APIPort:            8080,
		StoreDir:           "store",
		DownloadPaceMs:     250,
		MediaRetryTimeoutS: 30,
		PublicBaseURL:      "http://localhost:8080",

		AutoDownloadEnabled:   true,
		AutoDownloadWorkers:   2,
		AutoDownloadQueueSize: 100,
		AutoDownloadTimeoutS:  60,

		RetentionEnabled:         true,
		RetentionDryRun:          false,
		RetentionIntervalMinutes: 60,
		RetentionMaxPerSweep:     500,
	}

	if portStr := os.Getenv("API_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			cfg.APIPort = port
		}
	}

	if storeDir := os.Getenv("STORE_DIR"); storeDir != "" {
		cfg.StoreDir = storeDir
	}

	if paceStr := os.Getenv("DOWNLOAD_PACE_MS"); paceStr != "" {
		if pace, err := strconv.Atoi(paceStr); err == nil && pace >= 0 {
			cfg.DownloadPaceMs = pace
		}
	}

	if retryTimeoutStr := os.Getenv("MEDIA_RETRY_TIMEOUT_S"); retryTimeoutStr != "" {
		if timeout, err := strconv.Atoi(retryTimeoutStr); err == nil && timeout > 0 {
			cfg.MediaRetryTimeoutS = timeout
		}
	}

	if baseURL := os.Getenv("PUBLIC_BASE_URL"); baseURL != "" {
		cfg.PublicBaseURL = baseURL
	}

	// A kill switch for the whole pool, so auto-download can be stopped without touching
	// anyone's rules. Anything other than a recognised "off" value leaves it on.
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUTO_DOWNLOAD"))) {
	case "off", "false", "0", "no", "none":
		cfg.AutoDownloadEnabled = false
	}

	if workersStr := os.Getenv("AUTO_DOWNLOAD_WORKERS"); workersStr != "" {
		if workers, err := strconv.Atoi(workersStr); err == nil && workers > 0 {
			cfg.AutoDownloadWorkers = workers
		}
	}

	if queueStr := os.Getenv("AUTO_DOWNLOAD_QUEUE_SIZE"); queueStr != "" {
		if size, err := strconv.Atoi(queueStr); err == nil && size > 0 {
			cfg.AutoDownloadQueueSize = size
		}
	}

	if timeoutStr := os.Getenv("AUTO_DOWNLOAD_TIMEOUT_S"); timeoutStr != "" {
		if timeout, err := strconv.Atoi(timeoutStr); err == nil && timeout > 0 {
			cfg.AutoDownloadTimeoutS = timeout
		}
	}

	switch strings.ToLower(strings.TrimSpace(os.Getenv("RETENTION"))) {
	case "off", "false", "0", "no", "none":
		cfg.RetentionEnabled = false
	}

	// Retention is the only feature that deletes, so a mistake here must fail toward
	// keeping files: only an explicit "no" turns dry run off. Anything else that is set —
	// "true", but equally a typo like "ture" — means dry run.
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("RETENTION_DRY_RUN"))); v {
	case "":
		// unset: the default
	case "false", "0", "no", "off":
		cfg.RetentionDryRun = false
	default:
		cfg.RetentionDryRun = true
	}

	if v := os.Getenv("RETENTION_SWEEP_INTERVAL_M"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RetentionIntervalMinutes = n
		}
	}

	if v := os.Getenv("RETENTION_MAX_PER_SWEEP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RetentionMaxPerSweep = n
		}
	}

	return cfg
}
