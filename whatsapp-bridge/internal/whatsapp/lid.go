package whatsapp

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

var nonDigitRegex = regexp.MustCompile(`\D`)

// ResolveChatJIDs expands a phone number, @s.whatsapp.net JID, @lid JID, or @g.us JID
// into all corresponding chat JIDs that belong to the same conversation.
func ResolveChatJIDs(chat, storeDir string) ([]string, error) {
	chat = strings.TrimSpace(chat)
	if chat == "" {
		return nil, fmt.Errorf("empty chat identifier")
	}

	if storeDir == "" {
		storeDir = "store"
	}

	// Group or broadcast chat: no LID mapping
	if strings.HasSuffix(chat, "@g.us") || strings.HasSuffix(chat, "@broadcast") {
		return []string{chat}, nil
	}

	var user, server string
	if strings.Contains(chat, "@") {
		parts := strings.SplitN(chat, "@", 2)
		user = strings.SplitN(parts[0], ":", 2)[0]
		server = parts[1]
	} else {
		// Plain phone number passed
		user = nonDigitRegex.ReplaceAllString(chat, "")
		server = "s.whatsapp.net"
	}

	if user == "" {
		return nil, fmt.Errorf("cannot parse user from chat identifier: %s", chat)
	}

	jidsMap := make(map[string]bool)
	primaryJID := fmt.Sprintf("%s@%s", user, server)
	jidsMap[primaryJID] = true

	whatsappDBPath := filepath.Join(storeDir, "whatsapp.db")
	if _, err := os.Stat(whatsappDBPath); err == nil {
		dsn := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", whatsappDBPath)
		db, err := sql.Open("sqlite3", dsn)
		if err == nil {
			defer db.Close()

			if server == "lid" {
				rows, err := db.Query("SELECT pn FROM whatsmeow_lid_map WHERE lid = ?", user)
				if err == nil {
					defer rows.Close()
					for rows.Next() {
						var pn string
						if err := rows.Scan(&pn); err == nil && pn != "" {
							jidsMap[fmt.Sprintf("%s@s.whatsapp.net", pn)] = true
						}
					}
				}
			} else {
				rows, err := db.Query("SELECT lid FROM whatsmeow_lid_map WHERE pn = ?", user)
				if err == nil {
					defer rows.Close()
					for rows.Next() {
						var lid string
						if err := rows.Scan(&lid); err == nil && lid != "" {
							jidsMap[fmt.Sprintf("%s@lid", lid)] = true
						}
					}
				}
			}
		}
	}

	var jids []string
	for j := range jidsMap {
		jids = append(jids, j)
	}
	sort.Strings(jids)
	return jids, nil
}

// LIDMapReadable reports whether ResolveChatJIDs can consult the phone/LID map in storeDir.
// ResolveChatJIDs falls back to the identifier as typed when it cannot, silently; a caller
// that rejects input on the strength of that answer needs to know which one it got.
func LIDMapReadable(storeDir string) bool {
	if storeDir == "" {
		storeDir = "store"
	}
	path := filepath.Join(storeDir, "whatsapp.db")
	if _, err := os.Stat(path); err != nil {
		return false
	}
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", path))
	if err != nil {
		return false
	}
	defer db.Close()
	var n int
	return db.QueryRow("SELECT COUNT(*) FROM whatsmeow_lid_map LIMIT 1").Scan(&n) == nil
}
