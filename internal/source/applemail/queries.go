package applemail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/srhoton/renomail/internal/model"
)

// queryAccounts returns every account that owns an INBOX, each with a best-effort
// display name. Accounts are sorted (by display, then id) for deterministic output.
// A column/schema mismatch surfaces as a wrapped error rather than a panic, so an
// unexpected future Envelope Index layout degrades to an advisory warning upstream.
func queryAccounts(ctx context.Context, db *sql.DB) ([]Account, error) {
	rows, err := db.QueryContext(ctx, `SELECT ROWID, url FROM mailboxes WHERE url LIKE '%/INBOX'`)
	if err != nil {
		return nil, fmt.Errorf("list inbox mailboxes: %w", err)
	}
	defer rows.Close()

	type inbox struct {
		acctID string
		mbox   int64
	}
	var inboxes []inbox
	seen := make(map[string]struct{})
	for rows.Next() {
		var (
			mbox int64
			url  string
		)
		if err := rows.Scan(&mbox, &url); err != nil {
			return nil, fmt.Errorf("scan mailbox: %w", err)
		}
		acct, ok := accountFromURL(url)
		if !ok {
			continue
		}
		// Only a top-level INBOX (directly under the account) counts, matching
		// inboxMailbox's anchored lookup so every discovered account actually fetches;
		// a nested ".../Folder/INBOX" is ignored.
		if !strings.HasSuffix(url, "://"+acct+"/INBOX") {
			continue
		}
		if _, dup := seen[acct]; dup {
			continue
		}
		seen[acct] = struct{}{}
		inboxes = append(inboxes, inbox{acctID: acct, mbox: mbox})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mailboxes: %w", err)
	}

	accounts := make([]Account, 0, len(inboxes))
	for _, ib := range inboxes {
		// Display resolution is cosmetic: if its (best-effort) queries fail, fall back
		// to the short-UUID label rather than aborting discovery for the whole feature.
		display, err := resolveDisplay(ctx, db, ib.acctID, ib.mbox)
		if err != nil {
			display = "Apple Mail (" + shortID(ib.acctID) + ")"
		}
		accounts = append(accounts, Account{ID: ib.acctID, Display: display})
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Display != accounts[j].Display {
			return accounts[i].Display < accounts[j].Display
		}
		return accounts[i].ID < accounts[j].ID
	})
	return accounts, nil
}

// resolveDisplay derives a human label for an account: the address that sends its
// Sent mail (the owner's own address — reliable for iCloud/Exchange/IMAP), else the
// most common To-recipient of its INBOX, else a short UUID-based fallback.
func resolveDisplay(ctx context.Context, db *sql.DB, acctID string, inboxMbox int64) (string, error) {
	sent, err := singleString(ctx, db, `
		SELECT a.address
		FROM messages m
		JOIN mailboxes mb ON mb.ROWID = m.mailbox
		JOIN addresses a ON a.ROWID = m.sender
		WHERE mb.url LIKE '%://' || ? || '/%' AND mb.url LIKE '%Sent%'
		GROUP BY a.address ORDER BY COUNT(*) DESC LIMIT 1`, acctID)
	if err != nil {
		return "", fmt.Errorf("resolve account address (sent): %w", err)
	}
	if sent != "" {
		return sent, nil
	}

	// recipients.type 0 is the To: field; the account owner is its most frequent value.
	// INBOX membership is label-aware (see queryInbox): a Gmail inbox message is stored
	// in All Mail with a labels row pointing at the INBOX mailbox, so match both.
	clause, margs := inboxMembership(hasLabelsTable(ctx, db), inboxMbox)
	recip, err := singleString(ctx, db, `
		SELECT a.address
		FROM recipients r
		JOIN addresses a ON a.ROWID = r.address
		JOIN messages m ON m.ROWID = r.message
		WHERE `+clause+` AND r.type = 0
		GROUP BY a.address ORDER BY COUNT(*) DESC LIMIT 1`, margs...)
	if err != nil {
		return "", fmt.Errorf("resolve account address (inbox): %w", err)
	}
	if recip != "" {
		return recip, nil
	}
	return "Apple Mail (" + shortID(acctID) + ")", nil
}

// inboxMailbox returns the INBOX mailbox row id for an account, or 0 if absent in
// the current snapshot. The leading '%://' tolerates any URL scheme (imap, ews, …).
func inboxMailbox(ctx context.Context, db *sql.DB, acctID string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx,
		`SELECT ROWID FROM mailboxes WHERE url LIKE '%://' || ? || '/INBOX' ORDER BY ROWID LIMIT 1`,
		acctID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find inbox mailbox: %w", err)
	}
	return id, nil
}

// inboxMembership builds the SQL predicate (and its bind args) that tests whether a
// message row m belongs to the INBOX mailbox mbox. A real mailbox (iCloud/Exchange)
// stores its messages with m.mailbox = mbox; Gmail-over-IMAP stores them in
// [Gmail]/All Mail and records INBOX membership in the labels table. When the labels
// table is absent (an unexpected/old schema) it degrades to the plain mailbox test so
// non-label accounts still work instead of erroring the whole sweep.
func inboxMembership(hasLabels bool, mbox int64) (clause string, args []any) {
	if hasLabels {
		return "(m.mailbox = ? OR m.ROWID IN (SELECT message_id FROM labels WHERE mailbox_id = ?))", []any{mbox, mbox}
	}
	return "m.mailbox = ?", []any{mbox}
}

// hasLabelsTable reports whether the Envelope Index has a labels table (Gmail label
// membership). A missing table — or any lookup error — is treated as absent.
func hasLabelsTable(ctx context.Context, db *sql.DB) bool {
	var one int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'labels' LIMIT 1`).Scan(&one)
	return err == nil
}

// queryInbox returns the non-deleted INBOX messages at or after lowerUnix (epoch
// seconds), newest first, mapped to body-less items. INBOX membership is label-aware:
// a real mailbox (iCloud/Exchange) stores its messages with mailbox = the INBOX row,
// while Gmail over IMAP stores them in [Gmail]/All Mail and records INBOX membership in
// the labels table — so a message counts if either holds. The read flag is carried
// through so already-read mail is not surfaced as unread on the first sweep.
func queryInbox(ctx context.Context, db *sql.DB, p *Provider, mbox, lowerUnix int64, now time.Time) ([]model.Item, error) {
	clause, args := inboxMembership(hasLabelsTable(ctx, db), mbox)
	// COALESCE(m.read, 0) defends a NULL read flag (a Scan into bool would otherwise
	// fail and abort the whole sweep), matching the other nullable columns here.
	query := `
		SELECT m.ROWID, m.date_received, COALESCE(m.read, 0),
		       COALESCE(a.address, ''), COALESCE(a.comment, ''),
		       COALESCE(m.subject_prefix, ''), COALESCE(s.subject, ''),
		       COALESCE(su.summary, ''), COALESCE(g.message_id_header, '')
		FROM messages m
		LEFT JOIN addresses a ON a.ROWID = m.sender
		LEFT JOIN subjects s ON s.ROWID = m.subject
		LEFT JOIN summaries su ON su.ROWID = m.summary
		LEFT JOIN message_global_data g ON g.ROWID = m.global_message_id
		WHERE ` + clause + ` AND m.deleted = 0 AND m.date_received >= ?
		ORDER BY m.date_received DESC`
	rows, err := db.QueryContext(ctx, query, append(args, lowerUnix)...)
	if err != nil {
		return nil, fmt.Errorf("query inbox: %w", err)
	}
	defer rows.Close()

	// Seed a modest capacity so a busy cold-start scan does not repeatedly realloc.
	items := make([]model.Item, 0, 256)
	for rows.Next() {
		var (
			rowid                                             int64
			received                                          sql.NullInt64
			read                                              bool
			address, comment, subjectPrefix, subject, summary string
			msgID                                             string
		)
		if err := rows.Scan(&rowid, &received, &read, &address, &comment, &subjectPrefix, &subject, &summary, &msgID); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		items = append(items, p.toItem(rowid, received, read, address, comment, subjectPrefix, subject, summary, msgID, now))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return items, nil
}

// rowidByMessageID resolves a message row id from its RFC-822 Message-ID header,
// used by Body to find the .emlx file. It also returns the message's storage mailbox
// url so Body can scope its file search to the right mailbox — for a Gmail inbox
// message that is [Gmail]/All Mail, not INBOX. ok is false when no row matches.
func rowidByMessageID(ctx context.Context, db *sql.DB, messageID string) (rowid int64, mailboxURL string, ok bool, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT m.ROWID, COALESCE(mb.url, '')
		FROM messages m
		JOIN message_global_data g ON g.ROWID = m.global_message_id
		LEFT JOIN mailboxes mb ON mb.ROWID = m.mailbox
		WHERE g.message_id_header = ? LIMIT 1`, messageID).Scan(&rowid, &mailboxURL)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("find message by id: %w", err)
	}
	return rowid, mailboxURL, true, nil
}

// mailboxURLByRowid returns the storage mailbox url for a message row id. Body uses it
// to scope its .emlx search when the item carries a "rowid:" native id (no Message-ID),
// so even that path avoids an account-wide walk. An empty string means unknown.
func mailboxURLByRowid(ctx context.Context, db *sql.DB, rowid int64) (string, error) {
	var u sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT mb.url FROM messages m LEFT JOIN mailboxes mb ON mb.ROWID = m.mailbox WHERE m.ROWID = ? LIMIT 1`,
		rowid).Scan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find mailbox by rowid: %w", err)
	}
	return u.String, nil
}

// toItem maps one Envelope Index row onto the unified model.Item. The native id is
// the RFC-822 Message-ID (stable across an index rebuild) when present, else a
// "rowid:" fallback; the stable Item.ID derives from the source id and that native
// id so re-fetches upsert in place. read seeds the item's local read flag from Apple
// Mail (preserved by the store thereafter). The body is loaded lazily by Body.
func (p *Provider) toItem(rowid int64, received sql.NullInt64, read bool, address, comment, subjectPrefix, subject, summary, msgIDHeader string, now time.Time) model.Item {
	msgID := strings.TrimSpace(msgIDHeader)
	var nativeID, url string
	if msgID != "" {
		nativeID = msgID
		url = messageURL(msgID)
	} else {
		nativeID = "rowid:" + strconv.FormatInt(rowid, 10)
	}

	var published time.Time
	if received.Valid && received.Int64 > 0 {
		published = time.Unix(received.Int64, 0).UTC()
	}

	return model.Item{
		ID:         model.StableID(p.id, nativeID),
		Kind:       model.KindEmail,
		SourceID:   p.id,
		SourceName: p.name,
		Author:     formatSender(comment, address),
		Title:      formatSubject(subjectPrefix, subject),
		Snippet:    strings.TrimSpace(summary),
		URL:        url,
		NativeID:   nativeID,
		Published:  published,
		Fetched:    now,
		// Seeded from Apple Mail's read flag on the first upsert; the store preserves
		// local read changes thereafter (never written back to Apple Mail).
		Read: read,
	}
}

// accountFromURL extracts the Mail account UUID from a mailbox URL such as
// "imap://1D56…DF48/INBOX" — the segment between "://" and the next "/".
func accountFromURL(u string) (string, bool) {
	i := strings.Index(u, "://")
	if i < 0 {
		return "", false
	}
	rest := u[i+3:]
	j := strings.IndexByte(rest, '/')
	if j <= 0 {
		return "", false
	}
	return rest[:j], true
}

// messageURL builds a Mail.app "message://" deep link from an RFC-822 Message-ID,
// percent-encoding the angle brackets. The inner id rarely needs further escaping;
// this is best-effort "open in Mail.app" and an empty id yields no link.
func messageURL(messageID string) string {
	inner := strings.TrimSpace(messageID)
	inner = strings.TrimPrefix(inner, "<")
	inner = strings.TrimSuffix(inner, ">")
	if inner == "" {
		return ""
	}
	return "message://%3C" + inner + "%3E"
}

// formatSender renders a sender as "Name <addr>", "Name", or "addr" depending on
// what the addresses table provides (comment is the display name).
func formatSender(comment, address string) string {
	name := strings.TrimSpace(comment)
	addr := strings.TrimSpace(address)
	switch {
	case name != "" && addr != "":
		return name + " <" + addr + ">"
	case name != "":
		return name
	default:
		return addr
	}
}

// formatSubject joins the message's subject prefix ("Re:", "Fwd:") with the base
// subject from the subjects table.
func formatSubject(subjectPrefix, subject string) string {
	if p := strings.TrimSpace(subjectPrefix); p != "" {
		return p + " " + strings.TrimSpace(subject)
	}
	return strings.TrimSpace(subject)
}

// shortID returns a short, stable label for an account UUID used only in the
// fallback display name.
func shortID(acctID string) string {
	if len(acctID) > 8 {
		return acctID[:8]
	}
	return acctID
}

// singleString runs a query expected to return at most one string, returning "" for
// no rows.
func singleString(ctx context.Context, db *sql.DB, query string, args ...any) (string, error) {
	var s sql.NullString
	err := db.QueryRowContext(ctx, query, args...).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s.String), nil
}
