package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoForbiddenSQL is the standing grep from AUDIO_DOWNLOAD_PLAN.md §4.3/§4.2, kept as a
// test so it cannot quietly come back:
//
//   - "INSERT OR REPLACE INTO messages" is DELETE + INSERT, so a history-sync replay would
//     rewrite a row we already hold. StoreMessage uses ON CONFLICT ... COALESCE instead.
//   - "SELECT *" breaks old binaries against a migrated database, because the column count
//     changes underneath them. Every query names its columns.
func TestNoForbiddenSQL(t *testing.T) {
	forbidden := []struct {
		pattern string
		why     string
	}{
		{"INSERT OR REPLACE INTO messages", "rewrites existing rows on replay; use ON CONFLICT ... COALESCE (§4.3)"},
		{"SELECT *", "breaks old binaries after a migration; name the columns (§4.2)"},
	}

	root := filepath.Join("..", "..") // module root
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip vendored or build output if it ever appears
			if name := info.Name(); name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// This file names the patterns in order to forbid them
		if strings.HasSuffix(path, "sql_guard_test.go") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, f := range forbidden {
			if strings.Contains(string(src), f.pattern) {
				t.Errorf("%s contains %q: %s", path, f.pattern, f.why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
