package whatsapp

import (
	"errors"
	"fmt"
	"testing"

	"go.mau.fi/whatsmeow"
	"whatsapp-bridge/internal/database"
)

func TestExtractDirectPathFromURL(t *testing.T) {
	cases := []struct {
		url      string
		expected string
	}{
		{
			url:      "https://mmg.whatsapp.net/v/t62.7114-24/31693051_1189447385966442_1700685710609355745_n.enc?ccb=11-4&oh=01_Q5Aa...",
			expected: "/v/t62.7114-24/31693051_1189447385966442_1700685710609355745_n.enc?ccb=11-4&oh=01_Q5Aa...",
		},
		{
			// Shape of the URLs stored in messages.db: keep the auth tokens, drop mms3
			url:      "https://mmg.whatsapp.net/v/t62.7117-24/814407897_1104606052021007_7800582481540450805_n.enc?ccb=11-4&oh=01_abc&oe=6AD5D23F&_nc_sid=5e03e0&mms3=true",
			expected: "/v/t62.7117-24/814407897_1104606052021007_7800582481540450805_n.enc?ccb=11-4&oh=01_abc&oe=6AD5D23F&_nc_sid=5e03e0",
		},
		{
			url:      "https://mmg.whatsapp.net/v/t62/only_flag.enc?mms3=true",
			expected: "/v/t62/only_flag.enc",
		},
		{
			url:      "/v/t62/direct_only.enc",
			expected: "/v/t62/direct_only.enc",
		},
		{
			url:      "https://pps.whatsapp.net/v/t62/sample.enc",
			expected: "/v/t62/sample.enc",
		},
	}

	for _, c := range cases {
		got := ExtractDirectPathFromURL(c.url)
		if got != c.expected {
			t.Errorf("ExtractDirectPathFromURL(%q) = %q, expected %q", c.url, got, c.expected)
		}
	}
}

func TestSafePathComponent(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"972527872233@s.whatsapp.net", "972527872233_s_whatsapp_net"},
		{"120363239970576980@g.us", "120363239970576980_g_us"},
		{"../../etc/passwd", "______etc_passwd"},
		{"valid-name_123", "valid-name_123"},
	}

	for _, c := range cases {
		got := safePathComponent(c.input)
		if got != c.expected {
			t.Errorf("safePathComponent(%q) = %q, expected %q", c.input, got, c.expected)
		}
	}
}

func TestIsExpiredMediaErr(t *testing.T) {
	if !isExpiredMediaErr(whatsmeow.ErrMediaDownloadFailedWith403) {
		t.Error("expected true for ErrMediaDownloadFailedWith403")
	}
	if !isExpiredMediaErr(whatsmeow.ErrMediaDownloadFailedWith404) {
		t.Error("expected true for ErrMediaDownloadFailedWith404")
	}
	if !isExpiredMediaErr(whatsmeow.ErrMediaDownloadFailedWith410) {
		t.Error("expected true for ErrMediaDownloadFailedWith410")
	}
	if isExpiredMediaErr(errors.New("other network error")) {
		t.Error("expected false for other network error")
	}
	if isExpiredMediaErr(nil) {
		t.Error("expected false for nil")
	}
}

func TestRetryFailureStatus(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("re-uploaded media rejected: %w", errBlobMACMismatch), database.DownloadStatusRekeyedOnPhone},
		{fmt.Errorf("media retry answer: %w", whatsmeow.ErrMediaNotAvailableOnPhone), database.DownloadStatusNotOnPhone},
		{fmt.Errorf("media retry answer NOT_FOUND: %w", whatsmeow.ErrMediaNotAvailableOnPhone), database.DownloadStatusNotOnPhone},
		{fmt.Errorf("%w after 30s", errMediaRetryTimeout), database.DownloadStatusRetryTimeout},
		{fmt.Errorf("re-uploaded media rejected: %w", errBlobPlainMismatch), database.DownloadStatusFailed},
		{errors.New("context deadline exceeded"), database.DownloadStatusFailed},
	}
	for _, c := range cases {
		if got := retryFailureStatus(c.err); got != c.want {
			t.Errorf("retryFailureStatus(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestChatFolderKey(t *testing.T) {
	cases := []struct {
		jids []string
		want string
	}{
		// Existing folders under store/media must keep their names
		{[]string{"18674685612157@lid", "972523996302@s.whatsapp.net"}, "972523996302"},
		{[]string{"972523531857-1556783116@g.us"}, "972523531857-1556783116_g_us"},
		// A LID with no phone mapping: the LID user part, same for the batch and each download
		{[]string{"244332250583270@lid"}, "244332250583270"},
		{[]string{"../../etc@lid"}, "______etc"},
		{nil, "unknown"},
	}
	for _, c := range cases {
		if got := ChatFolderKey(c.jids); got != c.want {
			t.Errorf("ChatFolderKey(%v) = %q, want %q", c.jids, got, c.want)
		}
	}
}
