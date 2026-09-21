package whatsapp

import (
	"testing"

	"whatsapp-bridge/internal/database"
)

// liveURL and liveDirectPath are a real pair taken from a voice note stored after schema
// v1: the url column and the direct_path the protobuf carried for the same message.
const (
	liveURL        = "https://mmg.whatsapp.net/v/t62.7117-24/817341922_1847800626599067_6107761470274700032_n.enc?ccb=11-4&oh=01_Q5Aa5gFvc49qkSWa5ypzqBV41AJlYHl4CPBZv_rhF7uHfkevxQ&oe=6AD864E8&_nc_sid=5e03e0&mms3=true"
	liveDirectPath = "/v/t62.7117-24/817341922_1847800626599067_6107761470274700032_n.enc?ccb=11-4&oh=01_Q5Aa5gFvc49qkSWa5ypzqBV41AJlYHl4CPBZv_rhF7uHfkevxQ&oe=6AD864E8&_nc_sid=5e03e0"
)

// TestStoredDirectPathMatchesDerived is the reason preferring the stored column is safe:
// the protobuf's direct_path carries the same CDN query string (ccb/oh/oe/_nc_sid) that
// ExtractDirectPathFromURL keeps. If these ever diverge, swapping between them would
// silently drop the auth token — which is what bug S2 was.
func TestStoredDirectPathMatchesDerived(t *testing.T) {
	if got := ExtractDirectPathFromURL(liveURL); got != liveDirectPath {
		t.Errorf("derived direct path differs from the stored one:\n derived: %s\n stored:  %s", got, liveDirectPath)
	}
}

func TestResolveDirectPath(t *testing.T) {
	cases := []struct {
		name string
		info *database.MediaInfo
		want string
	}{
		{
			name: "schema v1 row uses the stored direct path",
			info: &database.MediaInfo{URL: liveURL, DirectPath: liveDirectPath},
			want: liveDirectPath,
		},
		{
			name: "pre-v1 row derives it from the url",
			info: &database.MediaInfo{URL: liveURL},
			want: liveDirectPath,
		},
		{
			name: "stored path wins even with no url",
			info: &database.MediaInfo{DirectPath: liveDirectPath},
			want: liveDirectPath,
		},
		{
			name: "nothing to go on",
			info: &database.MediaInfo{},
			want: "",
		},
		{
			name: "nil info",
			info: nil,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveDirectPath(tc.info); got != tc.want {
				t.Errorf("resolveDirectPath() = %q, want %q", got, tc.want)
			}
		})
	}
}
