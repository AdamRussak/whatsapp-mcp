package config

import (
	"os"
	"strconv"
)

// Config holds application configuration
type Config struct {
	APIPort             int
	StoreDir            string
	DownloadPaceMs      int
	MediaRetryTimeoutS  int
	PublicBaseURL       string
}

// NewConfig creates a new configuration with default values and environment overrides
func NewConfig() *Config {
	cfg := &Config{
		APIPort:            8080,
		StoreDir:           "store",
		DownloadPaceMs:     250,
		MediaRetryTimeoutS: 30,
		PublicBaseURL:      "http://localhost:8080",
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

	return cfg
}
