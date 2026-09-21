package config

import "testing"

// Retention deletes files, so its dry-run switch must fail toward keeping them: only an
// explicit "no" turns deletion on, and a typo means dry run.
func TestRetentionDryRunFailsSafe(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false}, // unset: the default
		{"true", true},
		{"1", true},
		{"yes", true},
		{"ture", true}, // a typo keeps files
		{"maybe", true},
		{"false", false},
		{"FALSE", false},
		{"0", false},
		{"no", false},
		{" off ", false},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("RETENTION_DRY_RUN", tc.value)
			if got := NewConfig().RetentionDryRun; got != tc.want {
				t.Fatalf("RETENTION_DRY_RUN=%q: dry run = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestRetentionKillSwitch(t *testing.T) {
	t.Setenv("RETENTION", "off")
	if NewConfig().RetentionEnabled {
		t.Fatal("RETENTION=off did not disable retention")
	}
	t.Setenv("RETENTION", "")
	if !NewConfig().RetentionEnabled {
		t.Fatal("retention should be enabled by default")
	}
}
