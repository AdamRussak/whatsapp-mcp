package whatsapp

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Chat naming (B12).
//
// A 1:1 chat is named after the other party. WhatsApp keeps what it knows about a person
// in whatsmeow_contacts, under BOTH of their JIDs, and the two carry different things:
//
//   - the phone JID (…@s.whatsapp.net) holds the name you saved them under — 688 of the
//     live store's full names are here;
//   - the LID (…@lid) usually holds only the push name they chose themselves — 7 full
//     names there, against 2,862 push names.
//
// Since 1:1 chats moved to LID addressing, the chat is a LID, so reading the contact under
// the chat's own JID finds no saved name. The old code then fell back to the message's
// sender — which, when the first stored message was your own, named the chat after you.
// 100 live chats carried the user's own LID as their name.
//
// PickChatName is the one place that decides; both the live writer (GetChatName) and the
// read-time display path (ContactDirectory) call it, so they cannot drift apart.

// ContactNames is what WhatsApp knows about a person under one of their JIDs.
type ContactNames struct {
	FirstName    string
	FullName     string
	PushName     string
	BusinessName string
}

// PickChatName chooses a 1:1 chat's display name.
//
// phone is the contact under their phone JID, lid the contact under their LID; either may
// be empty. number is their phone number when the LID map knows it. jidUser is the chat
// JID's user part, the last resort. It never returns anything derived from a message
// sender: in a 1:1 chat the other party is the chat itself.
//
// Order: the name you saved them under, then a business's verified name, then the name
// they chose, then their number — recognisable — and only then the opaque ID.
func PickChatName(phone, lid ContactNames, number, jidUser string) string {
	for _, candidate := range []string{
		phone.FullName, lid.FullName,
		phone.FirstName, lid.FirstName,
		phone.BusinessName, lid.BusinessName,
		phone.PushName, lid.PushName,
		number,
		jidUser,
	} {
		if name := strings.TrimSpace(candidate); name != "" {
			return name
		}
	}
	return ""
}

var bareNumber = regexp.MustCompile(`^\+?[0-9]+$`)

// NeedsName reports whether a stored chat name is a placeholder rather than a name: empty,
// or nothing but digits (a phone number, a LID, or — the B12 case — your own LID). A real
// name, including one someone saved on purpose, is left alone.
func NeedsName(stored string) bool {
	stored = strings.TrimSpace(stored)
	return stored == "" || bareNumber.MatchString(stored)
}

// isOneToOne reports whether a JID is a person's chat rather than a group or broadcast.
func isOneToOne(jid string) bool {
	return strings.HasSuffix(jid, "@s.whatsapp.net") || strings.HasSuffix(jid, "@lid")
}

func jidUser(jid string) string {
	user, _, _ := strings.Cut(jid, "@")
	user, _, _ = strings.Cut(user, ":") // drop a device suffix
	return user
}

// ContactDirectory answers naming and search questions from whatsapp.db for one request.
// It opens the database read-only once and batches its lookups, rather than once per chat
// (the B6 mistake). whatsapp.db belongs to whatsmeow: nothing here writes to it.
type ContactDirectory struct {
	db *sql.DB // nil when there is no whatsapp.db; every method then degrades to no-ops
}

// OpenContactDirectory opens <storeDir>/whatsapp.db read-only. A missing file is not an
// error: the directory simply knows nothing, and callers fall back to stored names.
func OpenContactDirectory(storeDir string) (*ContactDirectory, error) {
	if storeDir == "" {
		storeDir = "store"
	}
	path := filepath.Join(storeDir, "whatsapp.db")
	if _, err := os.Stat(path); err != nil {
		return &ContactDirectory{}, nil
	}
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", path))
	if err != nil {
		return nil, err
	}
	return &ContactDirectory{db: db}, nil
}

// Close releases the database handle.
func (d *ContactDirectory) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// maxInList keeps each IN (...) well under SQLite's bound-parameter limit.
const maxInList = 500

// inQuery runs query (which must end in "IN (") once per batch of values, appending the
// placeholders and the closing parenthesis, and hands each row to scan.
func (d *ContactDirectory) inQuery(query string, values []string, scan func(*sql.Rows) error) error {
	for start := 0; start < len(values); start += maxInList {
		end := start + maxInList
		if end > len(values) {
			end = len(values)
		}
		batch := values[start:end]
		placeholders := make([]string, len(batch))
		args := make([]interface{}, len(batch))
		for i, v := range batch {
			placeholders[i] = "?"
			args[i] = v
		}
		rows, err := d.db.Query(query+strings.Join(placeholders, ",")+")", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err := scan(rows); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// pairs resolves each 1:1 JID to its (phone JID, LID JID) pair through whatsmeow_lid_map.
// Either side is empty when unknown.
func (d *ContactDirectory) pairs(jids []string) (map[string][2]string, error) {
	out := make(map[string][2]string, len(jids))
	var lidUsers, phoneUsers []string
	for _, jid := range jids {
		switch {
		case strings.HasSuffix(jid, "@lid"):
			out[jid] = [2]string{"", jid}
			lidUsers = append(lidUsers, jidUser(jid))
		case strings.HasSuffix(jid, "@s.whatsapp.net"):
			out[jid] = [2]string{jid, ""}
			phoneUsers = append(phoneUsers, jidUser(jid))
		}
	}
	if d.db == nil {
		return out, nil
	}

	pnForLID := make(map[string]string)
	if err := d.inQuery(`SELECT lid, pn FROM whatsmeow_lid_map WHERE lid IN (`, lidUsers, func(r *sql.Rows) error {
		var lid, pn string
		if err := r.Scan(&lid, &pn); err != nil {
			return err
		}
		pnForLID[lid] = pn
		return nil
	}); err != nil {
		return nil, err
	}
	lidForPN := make(map[string]string)
	if err := d.inQuery(`SELECT lid, pn FROM whatsmeow_lid_map WHERE pn IN (`, phoneUsers, func(r *sql.Rows) error {
		var lid, pn string
		if err := r.Scan(&lid, &pn); err != nil {
			return err
		}
		lidForPN[pn] = lid
		return nil
	}); err != nil {
		return nil, err
	}

	for jid, pair := range out {
		if pair[1] != "" && pair[0] == "" {
			if pn := pnForLID[jidUser(jid)]; pn != "" {
				pair[0] = pn + "@s.whatsapp.net"
			}
		}
		if pair[0] != "" && pair[1] == "" {
			if lid := lidForPN[jidUser(jid)]; lid != "" {
				pair[1] = lid + "@lid"
			}
		}
		out[jid] = pair
	}
	return out, nil
}

// contacts reads whatsmeow_contacts for the given JIDs. A person can appear under more than
// one of our own device JIDs after a re-pair, so non-empty fields are merged.
func (d *ContactDirectory) contacts(jids []string) (map[string]ContactNames, error) {
	out := make(map[string]ContactNames, len(jids))
	if d.db == nil || len(jids) == 0 {
		return out, nil
	}
	err := d.inQuery(`SELECT their_jid, first_name, full_name, push_name, business_name
		FROM whatsmeow_contacts WHERE their_jid IN (`, jids, func(r *sql.Rows) error {
		var jid string
		var first, full, push, business sql.NullString
		if err := r.Scan(&jid, &first, &full, &push, &business); err != nil {
			return err
		}
		c := out[jid]
		if c.FirstName == "" {
			c.FirstName = first.String
		}
		if c.FullName == "" {
			c.FullName = full.String
		}
		if c.PushName == "" {
			c.PushName = push.String
		}
		if c.BusinessName == "" {
			c.BusinessName = business.String
		}
		out[jid] = c
		return nil
	})
	return out, err
}

// ChatIdentity is what the directory knows about a 1:1 chat's other party.
type ChatIdentity struct {
	Name  string // chosen by PickChatName
	Phone string // their number, when known; empty for groups and unmapped LIDs
}

// Identify returns display identities for chats, given the names stored in messages.db.
//
// Only 1:1 chats whose stored name is a placeholder (NeedsName) are renamed; a real stored
// name and every group are kept exactly as stored. Phone is filled for every 1:1 chat the
// LID map knows, so a UI can show the number beside the name.
func (d *ContactDirectory) Identify(stored map[string]string) (map[string]ChatIdentity, error) {
	out := make(map[string]ChatIdentity, len(stored))
	var people []string
	for jid, name := range stored {
		out[jid] = ChatIdentity{Name: name}
		if isOneToOne(jid) {
			people = append(people, jid)
		}
	}
	if len(people) == 0 {
		return out, nil
	}

	pairs, err := d.pairs(people)
	if err != nil {
		return nil, err
	}

	var lookup []string
	for _, pair := range pairs {
		for _, jid := range pair {
			if jid != "" {
				lookup = append(lookup, jid)
			}
		}
	}
	contacts, err := d.contacts(lookup)
	if err != nil {
		return nil, err
	}

	for _, jid := range people {
		pair := pairs[jid]
		number := ""
		if pair[0] != "" {
			number = jidUser(pair[0])
		}
		identity := out[jid]
		identity.Phone = number
		if NeedsName(identity.Name) {
			identity.Name = PickChatName(contacts[pair[0]], contacts[pair[1]], number, jidUser(jid))
		}
		out[jid] = identity
	}
	return out, nil
}

// SearchPeople returns the chat JIDs — both the phone and the LID form — of people whose
// saved, chosen or business name contains query, or whose phone number contains its
// digits. It is how "dudi" or "972527872233" finds 244332250583270@lid, a chat whose own
// ID and stored name contain neither.
func (d *ContactDirectory) SearchPeople(query string, limit int) ([]string, error) {
	if d.db == nil {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	found := make(map[string]bool)
	pattern := "%" + strings.ToLower(query) + "%"
	rows, err := d.db.Query(`SELECT DISTINCT their_jid FROM whatsmeow_contacts
		WHERE LOWER(full_name) LIKE ? OR LOWER(first_name) LIKE ?
		   OR LOWER(push_name) LIKE ? OR LOWER(business_name) LIKE ?
		LIMIT ?`, pattern, pattern, pattern, pattern, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			rows.Close()
			return nil, err
		}
		found[jid] = true
	}
	rows.Close()

	// A number: match it against the LID map's phone side, so a LID-only chat is found.
	// Five digits is the floor — shorter would match half the address book.
	if digits := nonDigitRegex.ReplaceAllString(query, ""); len(digits) >= 5 {
		rows, err := d.db.Query(`SELECT lid, pn FROM whatsmeow_lid_map WHERE pn LIKE ? LIMIT ?`,
			"%"+digits+"%", limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var lid, pn string
			if err := rows.Scan(&lid, &pn); err != nil {
				rows.Close()
				return nil, err
			}
			found[lid+"@lid"] = true
			found[pn+"@s.whatsapp.net"] = true
		}
		rows.Close()
	}

	// Every person matched under one JID is also wanted under the other, since a chat
	// may exist under either.
	var people []string
	for jid := range found {
		if isOneToOne(jid) {
			people = append(people, jid)
		}
	}
	pairs, err := d.pairs(people)
	if err != nil {
		return nil, err
	}
	for _, pair := range pairs {
		for _, jid := range pair {
			if jid != "" {
				found[jid] = true
			}
		}
	}

	out := make([]string, 0, len(found))
	for jid := range found {
		out = append(out, jid)
	}
	sort.Strings(out)
	return out, nil
}
