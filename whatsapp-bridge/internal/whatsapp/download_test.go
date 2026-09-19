package whatsapp

import (
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"
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
