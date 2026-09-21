package database

import (
	"database/sql"
	"fmt"
)

// migration is one versioned, additive change to messages.db.
//
// Rules (AUDIO_DOWNLOAD_PLAN.md §4.2): additive only — never drop, rename, retype or
// change a constraint. Each statement is skipped when it has already been applied, so a
// migration can be re-run safely. One transaction per version.
type migration struct {
	version int
	name    string
	apply   func(tx *sql.Tx) error
}

// migrations is the ordered list of schema versions. Append only; never edit or renumber
// an entry that has shipped.
var migrations = []migration{
	{
		version: 1,
		name:    "audio metadata columns",
		apply: func(tx *sql.Tx) error {
			// Voice-note metadata that ExtractMediaInfo now captures from the protobuf.
			// Download state (local_path, downloaded_at, status) deliberately lives in
			// media.db only, so download code stays SELECT-only on this database.
			return addColumns(tx, "messages", []column{
				{"direct_path", "TEXT"},
				{"mimetype", "TEXT"},
				{"seconds", "INTEGER"},
				{"is_ptt", "BOOLEAN"},
			})
		},
	},
	{
		version: 2,
		name:    "auto-download action on webhook configs",
		apply: func(tx *sql.Tx) error {
			// Auto-download is an action on the trigger rules that already exist, not a
			// second rule engine: a listener is an ordinary webhook config with these on.
			// createTables carries the same two columns, so on a fresh database this is a
			// no-op (addColumns skips what PRAGMA table_info already shows).
			return addColumns(tx, "webhook_configs", []column{
				{"auto_download", "BOOLEAN DEFAULT 0"},
				{"auto_download_voice_only", "BOOLEAN DEFAULT 1"},
			})
		},
	},
	{
		version: 3,
		name:    "event type on webhook logs",
		apply: func(tx *sql.Tx) error {
			// Auto-download outcomes are logged alongside deliveries so the UI's existing
			// per-listener log view can show them. Existing rows are deliveries, which is
			// what the default says.
			return addColumns(tx, "webhook_logs", []column{
				{"event_type", "TEXT DEFAULT 'delivery'"},
			})
		},
	},
	{
		version: 4,
		name:    "per-listener retention",
		apply: func(tx *sql.Tx) error {
			// Days a listener's captures are kept before retention removes the files. The
			// default, 0, means keep forever: no existing listener starts deleting anything.
			return addColumns(tx, "webhook_configs", []column{
				{"retention_days", "INTEGER DEFAULT 0"},
			})
		},
	},
}

type column struct {
	name string
	typ  string
}

// addColumns adds each column that is not already present. Types carry no default, so
// existing rows read NULL and no row is rewritten.
func addColumns(tx *sql.Tx, table string, cols []column) error {
	existing, err := tableColumns(tx, table)
	if err != nil {
		return err
	}
	for _, col := range cols {
		if existing[col.name] {
			continue
		}
		// Table and column names are compile-time constants from the list above, never input.
		if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col.name, col.typ)); err != nil {
			return fmt.Errorf("add %s.%s: %w", table, col.name, err)
		}
	}
	return nil
}

// tableColumns returns the set of column names on a table.
func tableColumns(tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, fmt.Errorf("table_info(%s): %w", table, err)
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// runMigrations applies every migration newer than the recorded schema version.
//
// It is called from NewMessageStoreWithDir, so it completes before the HTTP listener
// starts. A failure is returned, not logged and swallowed: the caller exits non-zero
// rather than serving on a half-migrated database.
func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return fmt.Errorf("migration v%d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

// applyMigration runs one migration and records its version in the same transaction, so
// the two can never disagree.
func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := m.apply(tx); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.version); err != nil {
		return err
	}
	return tx.Commit()
}

// appliedVersions reads the versions already recorded.
func appliedVersions(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query("SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// SchemaVersion returns the highest applied migration version (0 on a fresh database).
func (store *MessageStore) SchemaVersion() (int, error) {
	var v sql.NullInt64
	err := store.db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&v)
	if err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}
