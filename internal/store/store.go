// Package store implements all persistence logic over SQLite.
// Handlers stay thin; every data decision lives here.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"
)

// DefaultPVRetention bounds the raw pageviews table: older rows are rolled
// into daily_stats and deleted, so the hot layer never outgrows this window.
const DefaultPVRetention = 30 * 24 * time.Hour

// Store wraps the SQL database. now is injectable for tests (clock fake).
type Store struct {
	db  *sql.DB
	now func() time.Time

	// PVRetention is how long raw pageview rows live before compaction.
	// Windows that fit inside it are answered from raw data exactly (to the
	// timestamp); wider windows merge the permanent daily_stats layer.
	PVRetention time.Duration
}

// New creates a Store over an already-migrated database.
func New(d *sql.DB) *Store {
	return &Store{db: d, now: time.Now, PVRetention: DefaultPVRetention}
}

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// NewToken returns 32 crypto/rand bytes hex-encoded (64 chars).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashIP implements the privacy rule: sha256(ip + YYYY-MM-DD) 的前 16 个十六进
// 制字符（64 bit），按日轮换。截到 64 bit 是刻意的行宽瘦身——按日去重的量级下
// 碰撞概率可忽略，一行省 48 字节。
func HashIP(ip string, t time.Time) string {
	sum := sha256.Sum256([]byte(ip + t.UTC().Format("2006-01-02")))
	return hex.EncodeToString(sum[:8])
}

// deviceClass collapses a User-Agent string (100–300B) into one character:
// m=mobile/tablet, d=desktop, b=bot/crawler, o=other. The stats hot layer
// only ever needs "what kind of device", never the full UA.
func deviceClass(ua string) string {
	u := strings.ToLower(ua)
	switch {
	case strings.Contains(u, "bot"), strings.Contains(u, "spider"),
		strings.Contains(u, "crawl"), strings.Contains(u, "slurp"),
		strings.Contains(u, "headless"):
		return "b"
	case strings.Contains(u, "mobile"), strings.Contains(u, "iphone"),
		strings.Contains(u, "ipad"), strings.Contains(u, "android"):
		return "m"
	case strings.Contains(u, "windows"), strings.Contains(u, "macintosh"),
		strings.Contains(u, "x11"), strings.Contains(u, "linux"):
		return "d"
	default:
		return "o"
	}
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Subscriber is a row of the subscribers table.
type Subscriber struct {
	ID               int64
	Email            string
	Status           string
	ConfirmToken     string
	ConfirmExpiresAt int64
	UnsubToken       string
	CreatedAt        int64
	ConfirmedAt      int64
	UpdatedAt        int64
}

// UpsertPending inserts a new pending subscriber or, when the email exists in
// a non-active state, refreshes its confirm token and returns it to pending.
// An already-active row is returned unchanged.
func (s *Store) UpsertPending(email, confirmToken string, expires time.Time) (*Subscriber, error) {
	email = normalizeEmail(email)
	now := s.now().Unix()
	unsub, err := NewToken()
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(
		`INSERT INTO subscribers (email, status, confirm_token, confirm_expires_at, unsub_token, created_at, updated_at)
		 VALUES (?, 'pending', ?, ?, ?, ?, ?)
		 ON CONFLICT(email) DO UPDATE SET
		   confirm_token = excluded.confirm_token,
		   confirm_expires_at = excluded.confirm_expires_at,
		   status = CASE WHEN subscribers.status = 'active' THEN 'active' ELSE 'pending' END,
		   updated_at = excluded.updated_at`,
		email, confirmToken, expires.Unix(), unsub, now, now)
	if err != nil {
		return nil, fmt.Errorf("upsert subscriber: %w", err)
	}
	return s.GetSubscriberByEmail(email)
}

// GetSubscriberByEmail looks up a subscriber (email is normalized first).
func (s *Store) GetSubscriberByEmail(email string) (*Subscriber, error) {
	row := s.db.QueryRow(
		`SELECT `+subscriberCols+`
		        FROM subscribers WHERE email = ?`, normalizeEmail(email))
	return scanSubscriber(row)
}

// GetSubscriberByUnsubToken looks up a subscriber by unsubscribe token.
func (s *Store) GetSubscriberByUnsubToken(token string) (*Subscriber, error) {
	row := s.db.QueryRow(
		`SELECT `+subscriberCols+`
		        FROM subscribers WHERE unsub_token = ?`, token)
	return scanSubscriber(row)
}

// Ping checks database reachability (healthz).
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// ConfirmSubscriber activates a pending subscriber by confirm token.
// Expired or unknown tokens return an error.
func (s *Store) ConfirmSubscriber(token string) (*Subscriber, error) {
	row := s.db.QueryRow(
		`SELECT `+subscriberCols+`
		        FROM subscribers WHERE confirm_token = ? AND status = 'pending'`, token)
	sub, err := scanSubscriber(row)
	if err != nil {
		return nil, err
	}
	if sub.ConfirmExpiresAt < s.now().Unix() {
		return nil, fmt.Errorf("confirm token expired")
	}
	now := s.now().Unix()
	if _, err := s.db.Exec(
		`UPDATE subscribers SET status='active', confirmed_at=?, updated_at=? WHERE id=?`,
		now, now, sub.ID); err != nil {
		return nil, fmt.Errorf("activate subscriber: %w", err)
	}
	sub.Status = "active"
	sub.ConfirmedAt = now
	return sub, nil
}

// Activate marks a subscriber active and stamps confirmed_at/updated_at.
func (s *Store) Activate(id int64) error {
	now := s.now().Unix()
	res, err := s.db.Exec(
		`UPDATE subscribers SET status='active', confirmed_at=?, updated_at=? WHERE id=?`, now, now, id)
	if err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UnsubscribeByToken sets status=unsubscribed for the token's owner.
func (s *Store) UnsubscribeByToken(token string) error {
	res, err := s.db.Exec(
		`UPDATE subscribers SET status='unsubscribed', updated_at=? WHERE unsub_token=?`,
		s.now().Unix(), token)
	if err != nil {
		return fmt.Errorf("unsubscribe: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSubscribers pages the subscriber list; status and q (email substring) filter.
func (s *Store) ListSubscribers(status, q string, offset, limit int) ([]Subscriber, error) {
	where, args := subscriberWhere(status, q)
	args = append(args, limit, offset)
	rows, err := s.db.Query(
		`SELECT `+subscriberCols+`
		        FROM subscribers `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list subscribers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Subscriber
	for rows.Next() {
		sub, err := scanSubscriberRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sub)
	}
	return out, rows.Err()
}

// CountSubscribers counts with the same filters as ListSubscribers.
func (s *Store) CountSubscribers(status, q string) (int, error) {
	where, args := subscriberWhere(status, q)
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM subscribers `+where, args...).Scan(&n)
	return n, err
}

func subscriberWhere(status, q string) (string, []any) {
	var conds []string
	var args []any
	if status != "" {
		conds = append(conds, "status = ?")
		args = append(args, status)
	}
	if q != "" {
		conds = append(conds, "email LIKE ?")
		args = append(args, "%"+q+"%")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// ActiveSubscribers returns every active subscriber (newsletter fan-out).
func (s *Store) ActiveSubscribers() ([]Subscriber, error) {
	rows, err := s.db.Query(
		`SELECT ` + subscriberCols + `
		        FROM subscribers WHERE status='active' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("active subscribers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Subscriber
	for rows.Next() {
		sub, err := scanSubscriberRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sub)
	}
	return out, rows.Err()
}

// DeleteSubscriber removes a subscriber row.
func (s *Store) DeleteSubscriber(id int64) error {
	res, err := s.db.Exec(`DELETE FROM subscribers WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete subscriber: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ImportSubscribers bulk-activates pre-verified emails (legacy platform
// migration): each becomes active with confirmed_at set; duplicates skip.
// Rows that fail net/mail parsing are skipped and counted as invalid.
// The whole batch commits as one transaction — a per-row implicit commit
// would fsync once per email on a large CSV.
func (s *Store) ImportSubscribers(emails []string) (imported, skipped, invalid int, err error) {
	now := s.now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("import begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, raw := range emails {
		email := normalizeEmail(raw)
		addr, perr := mail.ParseAddress(email)
		if email == "" {
			invalid++
			continue
		}
		if perr != nil || addr.Address != email || !strings.Contains(email, "@") {
			invalid++
			continue
		}
		unsub, terr := NewToken()
		if terr != nil {
			return imported, skipped, invalid, terr
		}
		res, terr := tx.Exec(
			`INSERT OR IGNORE INTO subscribers
			   (email, status, confirm_token, confirm_expires_at, unsub_token, created_at, confirmed_at, updated_at)
			 VALUES (?, 'active', '', 0, ?, ?, ?, ?)`,
			email, unsub, now, now, now)
		if terr != nil {
			return imported, skipped, invalid, fmt.Errorf("import %s: %w", email, terr)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			imported++
		} else {
			skipped++
		}
	}
	if err = tx.Commit(); err != nil {
		return imported, skipped, invalid, fmt.Errorf("import commit: %w", err)
	}
	return imported, skipped, invalid, nil
}

// Comment is a row of the comments table.
type Comment struct {
	ID          int64
	PostSlug    string
	AuthorName  string
	AuthorEmail string
	Content     string
	Status      string
	IPHash      string
	UA          string
	CreatedAt   int64
	ModeratedAt int64
}

// CreateComment inserts a pending comment and returns its id.
func (s *Store) CreateComment(postSlug, name, email, content, ipHash, ua string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO comments (post_slug, author_name, author_email, content, status, ip_hash, ua, created_at)
		 VALUES (?, ?, ?, ?, 'pending', ?, ?, ?)`,
		postSlug, name, email, content, ipHash, ua, s.now().Unix())
	if err != nil {
		return 0, fmt.Errorf("create comment: %w", err)
	}
	return res.LastInsertId()
}

// GetComment fetches one comment by id.
func (s *Store) GetComment(id int64) (*Comment, error) {
	row := s.db.QueryRow(
		`SELECT `+commentCols+`
			 FROM comments WHERE id=?`, id)
	c, err := scanComment(row)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ModerateComment sets status to approved|rejected with moderated_at.
func (s *Store) ModerateComment(id int64, status string) error {
	if status != "approved" && status != "rejected" {
		return fmt.Errorf("invalid moderation status %q", status)
	}
	res, err := s.db.Exec(
		`UPDATE comments SET status=?, moderated_at=? WHERE id=?`, status, s.now().Unix(), id)
	if err != nil {
		return fmt.Errorf("moderate comment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListApprovedComments returns the public projection (no email/ip/ua) of a
// post's approved comments, chronological.
func (s *Store) ListApprovedComments(postSlug string) ([]Comment, error) {
	rows, err := s.db.Query(
		`SELECT id, post_slug, author_name, content, created_at
		 FROM comments WHERE post_slug=? AND status='approved'
		 ORDER BY created_at ASC, id ASC`, postSlug)
	if err != nil {
		return nil, fmt.Errorf("list approved comments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.PostSlug, &c.AuthorName, &c.Content, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListComments returns comments for the admin console, filtered by status
// (empty = all), newest first.
func (s *Store) ListComments(status string) ([]Comment, error) {
	q := `SELECT ` + commentCols + `
			 FROM comments`
	var args []any
	if status != "" {
		q += ` WHERE status=?`
		args = append(args, status)
	}
	q += ` ORDER BY id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Comment
	for rows.Next() {
		c, err := scanCommentRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// CountComments counts comments by status (empty = all).
func (s *Store) CountComments(status string) (int, error) {
	var n int
	var err error
	if status == "" {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&n)
	} else {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM comments WHERE status=?`, status).Scan(&n)
	}
	return n, err
}

// Newsletter is a row of the newsletters table.
type Newsletter struct {
	ID          int64
	Subject     string
	Markdown    string
	HTML        string
	PostURL     string
	Status      string
	SentCount   int
	FailedCount int
	CreatedAt   int64
	SentAt      int64
}

// CreateNewsletter stores a rendered draft. postURL is the source article
// URL for imported drafts ('' for hand-written ones).
func (s *Store) CreateNewsletter(subject, markdown, html, postURL string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO newsletters (subject, markdown, html, post_url, status, created_at)
		 VALUES (?, ?, ?, ?, 'draft', ?)`, subject, markdown, html, postURL, s.now().Unix())
	if err != nil {
		return 0, fmt.Errorf("create newsletter: %w", err)
	}
	return res.LastInsertId()
}

// GetNewsletter fetches one newsletter by id.
func (s *Store) GetNewsletter(id int64) (*Newsletter, error) {
	row := s.db.QueryRow(
		`SELECT `+newsletterCols+`
			 FROM newsletters WHERE id=?`, id)
	n, err := scanNewsletter(row)
	if err != nil {
		return nil, err
	}
	return n, nil
}

// ListNewsletters returns all newsletters, newest first.
func (s *Store) ListNewsletters() ([]Newsletter, error) {
	rows, err := s.db.Query(
		`SELECT ` + newsletterCols + `
			 FROM newsletters ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list newsletters: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Newsletter
	for rows.Next() {
		n, err := scanNewsletterRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// MarkNewsletterSending transitions draft → sending.
func (s *Store) MarkNewsletterSending(id int64) error {
	res, err := s.db.Exec(`UPDATE newsletters SET status='sending' WHERE id=? AND status='draft'`, id)
	if err != nil {
		return fmt.Errorf("mark sending: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("newsletter %d not in draft state", id)
	}
	return nil
}

// MarkNewsletterSent records final counts (accumulated across partial runs)
// and sent_at, closing the newsletter as fully delivered.
func (s *Store) MarkNewsletterSent(id int64, sent, failed int) error {
	res, err := s.db.Exec(
		`UPDATE newsletters SET status='sent',
		        sent_count=sent_count+?, failed_count=failed_count+?, sent_at=?
		 WHERE id=?`,
		sent, failed, s.now().Unix(), id)
	if err != nil {
		return fmt.Errorf("mark sent: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevertOneToDraft returns a partially-delivered newsletter from sending back
// to draft (recipients remain for a follow-up run), accumulating this run's
// counts. Only valid while status='sending'.
func (s *Store) RevertOneToDraft(id int64, sent, failed int) error {
	res, err := s.db.Exec(
		`UPDATE newsletters SET status='draft',
		        sent_count=sent_count+?, failed_count=failed_count+?
		 WHERE id=? AND status='sending'`,
		sent, failed, id)
	if err != nil {
		return fmt.Errorf("revert one to draft: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("newsletter %d not in sending state", id)
	}
	return nil
}

// MarkNewsletterFailed transitions to failed (catastrophic send error).
func (s *Store) MarkNewsletterFailed(id int64) error {
	res, err := s.db.Exec(`UPDATE newsletters SET status='failed' WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevertSendingToDraft recovers newsletters stuck in 'sending' after a
// restart (idempotent per SPEC §4: 宁重发不丢).
func (s *Store) RevertSendingToDraft() (int64, error) {
	res, err := s.db.Exec(`UPDATE newsletters SET status='draft' WHERE status='sending'`)
	if err != nil {
		return 0, fmt.Errorf("revert sending: %w", err)
	}
	return res.RowsAffected()
}

// InsertSendLog records one per-recipient delivery attempt.
func (s *Store) InsertSendLog(newsletterID, subscriberID int64, status, errMsg string) error {
	_, err := s.db.Exec(
		`INSERT INTO send_log (newsletter_id, subscriber_id, status, error, created_at)
		 VALUES (?, ?, ?, ?, ?)`, newsletterID, subscriberID, status, errMsg, s.now().Unix())
	if err != nil {
		return fmt.Errorf("insert send_log: %w", err)
	}
	return nil
}

// SendLogEntry is a row of send_log.
type SendLogEntry struct {
	ID           int64
	NewsletterID int64
	SubscriberID int64
	Status       string
	Error        string
	CreatedAt    int64
}

// ListSendLog returns log rows for one newsletter.
func (s *Store) ListSendLog(newsletterID int64) ([]SendLogEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, newsletter_id, subscriber_id, status, error, created_at
		 FROM send_log WHERE newsletter_id=? ORDER BY id`, newsletterID)
	if err != nil {
		return nil, fmt.Errorf("list send_log: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SendLogEntry
	for rows.Next() {
		var e SendLogEntry
		if err := rows.Scan(&e.ID, &e.NewsletterID, &e.SubscriberID, &e.Status, &e.Error, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SubscriberSendState pairs an active subscriber with whether this
// newsletter was already delivered to them (send_log status='sent').
type SubscriberSendState struct {
	Subscriber Subscriber
	Sent       bool
}

// SubscribersWithSendState lists every active subscriber with a sent flag for
// the given newsletter — the recipient picker and the "all delivered?" check
// share this one query. Failed attempts do not count as sent (retryable).
func (s *Store) SubscribersWithSendState(newsletterID int64) ([]SubscriberSendState, error) {
	rows, err := s.db.Query(
		`SELECT `+subscriberCols+`,
		        EXISTS (SELECT 1 FROM send_log
		                 WHERE send_log.newsletter_id=? AND send_log.subscriber_id=subscribers.id
		                   AND send_log.status='sent') AS sent
		 FROM subscribers WHERE status='active' ORDER BY id`, newsletterID)
	if err != nil {
		return nil, fmt.Errorf("subscribers with send state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SubscriberSendState
	for rows.Next() {
		var sub Subscriber
		var sent bool
		// 列顺序 = subscriberCols + sent 尾列（sent 必须与 SELECT 尾部对齐）。
		if err := rows.Scan(&sub.ID, &sub.Email, &sub.Status, &sub.ConfirmToken, &sub.ConfirmExpiresAt,
			&sub.UnsubToken, &sub.CreatedAt, &sub.ConfirmedAt, &sub.UpdatedAt, &sent); err != nil {
			return nil, fmt.Errorf("scan subscriber send state: %w", err)
		}
		out = append(out, SubscriberSendState{Subscriber: sub, Sent: sent})
	}
	return out, rows.Err()
}

// SentSubscriberIDs returns the set of subscriber ids this newsletter was
// already delivered to (send_log status='sent'), for send-loop dedup.
func (s *Store) SentSubscriberIDs(newsletterID int64) (map[int64]bool, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT subscriber_id FROM send_log
		 WHERE newsletter_id=? AND status='sent'`, newsletterID)
	if err != nil {
		return nil, fmt.Errorf("sent subscriber ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan sent id: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// RecordDownloadDedup records a download unless the same (slug, ip_hash)
// already has a row on the current UTC day. Returns true when counted.
// The check and the insert are one atomic statement: two statements would
// interleave between pooled connections and double-count.
func (s *Store) RecordDownloadDedup(slug, ipHash, ua, referer string) (bool, error) {
	now := s.now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()
	res, err := s.db.Exec(
		`INSERT INTO downloads (slug, ip_hash, ua, referer, created_at)
		 SELECT ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (
		   SELECT 1 FROM downloads WHERE slug=? AND ip_hash=? AND created_at>=?
		 )`,
		slug, ipHash, ua, referer, now.Unix(), slug, ipHash, dayStart)
	if err != nil {
		return false, fmt.Errorf("record download: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("record download rows: %w", err)
	}
	return n > 0, nil
}

// DownloadStats counts downloads per slug since the given epoch second.
func (s *Store) DownloadStats(since int64) (map[string]int, error) {
	rows, err := s.db.Query(
		`SELECT slug, COUNT(*) FROM downloads WHERE created_at>=? GROUP BY slug ORDER BY slug`, since)
	if err != nil {
		return nil, fmt.Errorf("download stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var slug string
		var n int
		if err := rows.Scan(&slug, &n); err != nil {
			return nil, err
		}
		out[slug] = n
	}
	return out, rows.Err()
}

// RecordPageview inserts one pageview row. site is the beacon origin host
// ("" = the blog itself). Only the device class of the UA is stored — the hot
// layer never needs the full string.
func (s *Store) RecordPageview(site, path, referrer, ipHash, ua string) error {
	_, err := s.db.Exec(
		`INSERT INTO pageviews (site, path, referrer, ip_hash, ua, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		site, path, referrer, ipHash, deviceClass(ua), s.now().Unix())
	if err != nil {
		return fmt.Errorf("record pageview: %w", err)
	}
	return nil
}

// CompactPageviews rolls complete days of raw pageviews into daily_stats,
// then deletes rows older than retention — in that order, in one transaction,
// so no day is deleted before it is aggregated. Today (an incomplete day,
// by the passed clock's localtime) is neither rolled nor pruned. Rolled
// values are exact recomputations (same-day ip_hash dedup), so re-running is
// idempotent; rollup rows survive their raw data being pruned. Returns days
// rolled and rows pruned.
//
// now is a parameter (not s.now()) so the caller — the stats Compactor —
// owns the clock end to end; a Compactor with a frozen test clock used to
// leak the store's real clock here and roll "today" once reality passed the
// frozen date (2026-11-08 CI detonation).
func (s *Store) CompactPageviews(now time.Time, retention time.Duration) (rolled, pruned int64, err error) {
	if retention < 2*24*time.Hour {
		return 0, 0, fmt.Errorf("pageview retention %s too short: a day must fit whole", retention)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("compact pageviews: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 结日（localtime）聚合进永久层；upsert 保证重跑是重算而非叠加。
	res, err := tx.Exec(
		`INSERT INTO daily_stats (site, day, pv, uv)
		 SELECT site, date(created_at,'unixepoch','localtime'), COUNT(*), COUNT(DISTINCT ip_hash)
		 FROM pageviews
		 WHERE date(created_at,'unixepoch','localtime') < date(?,'unixepoch','localtime')
		 GROUP BY 1, 2
		 ON CONFLICT(site, day) DO UPDATE SET pv=excluded.pv, uv=excluded.uv`,
		now.Unix())
	if err != nil {
		return 0, 0, fmt.Errorf("rollup pageviews: %w", err)
	}
	if rolled, err = res.RowsAffected(); err != nil {
		return 0, 0, fmt.Errorf("rollup rows affected: %w", err)
	}

	res, err = tx.Exec(
		`DELETE FROM pageviews WHERE created_at < ?`, now.Add(-retention).Unix())
	if err != nil {
		return 0, 0, fmt.Errorf("prune pageviews: %w", err)
	}
	if pruned, err = res.RowsAffected(); err != nil {
		return 0, 0, fmt.Errorf("prune rows affected: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("compact pageviews commit: %w", err)
	}
	return rolled, pruned, nil
}

// pvRetention returns the effective retention window (zero value = default).
func (s *Store) pvRetention() time.Duration {
	if s.PVRetention <= 0 {
		return DefaultPVRetention
	}
	return s.PVRetention
}

// useRollupStats reports whether a since-window reaches beyond the raw
// retention window: raw data alone can no longer answer it, so queries merge
// the permanent daily_stats layer. Windows inside retention keep the exact
// raw semantics (down to the timestamp, not the day).
func (s *Store) useRollupStats(since int64) bool {
	return since < s.now().Add(-s.pvRetention()).Unix()
}

// PageviewStats returns PV and UV since epoch second, scoped to one site
// ("" = the blog). UV 按日加盐意味着跨日访客本就不可链接：多日 UV = 各日去重
// 之和（uv 跨日可加，语义与旧口径一致）。窗口落在保留期内走 raw 精确路径；
// 更宽的窗口合并 daily_stats 永久层 + 尚未聚合的 raw 天（rollup 缺位时自愈）。
// raw 兜底按 (site, day) 对排除——他站同日的 rollup 行不得误伤本站 raw。
func (s *Store) PageviewStats(site string, since int64) (pv, uv int, err error) {
	if !s.useRollupStats(since) {
		err = s.db.QueryRow(
			`SELECT COUNT(*), COUNT(DISTINCT ip_hash) FROM pageviews
			 WHERE site=? AND created_at>=?`, site, since).
			Scan(&pv, &uv)
		if err != nil {
			return 0, 0, fmt.Errorf("pageview stats: %w", err)
		}
		return pv, uv, nil
	}

	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(pv),0), COALESCE(SUM(uv),0) FROM daily_stats
		 WHERE site=? AND day >= date(?,'unixepoch','localtime')`, site, since).
		Scan(&pv, &uv); err != nil {
		return 0, 0, fmt.Errorf("pageview stats rollup: %w", err)
	}
	var rpv, ruv int
	if err := s.db.QueryRow(
		`SELECT COUNT(*), COUNT(DISTINCT ip_hash) FROM pageviews
		 WHERE site=? AND created_at>=?
		   AND NOT EXISTS (SELECT 1 FROM daily_stats d
		                   WHERE d.site=pageviews.site
		                     AND d.day=date(pageviews.created_at,'unixepoch','localtime'))`,
		site, since).Scan(&rpv, &ruv); err != nil {
		return 0, 0, fmt.Errorf("pageview stats raw tail: %w", err)
	}
	return pv + rpv, uv + ruv, nil
}

// Media is a row of the media table.
type Media struct {
	ID          int64
	Key         string
	URL         string
	Bytes       int64
	ContentType string
	CreatedAt   int64
}

// CreateMedia records an uploaded object.
func (s *Store) CreateMedia(key, url string, bytes int64, contentType string) error {
	_, err := s.db.Exec(
		`INSERT INTO media (key, url, bytes, content_type, created_at) VALUES (?, ?, ?, ?, ?)`,
		key, url, bytes, contentType, s.now().Unix())
	if err != nil {
		return fmt.Errorf("create media: %w", err)
	}
	return nil
}

// GetMediaByKey fetches one media row by object key.
func (s *Store) GetMediaByKey(key string) (*Media, error) {
	row := s.db.QueryRow(
		`SELECT `+mediaCols+` FROM media WHERE key=?`, key)
	m, err := scanMedia(row)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ListMedia returns all media, newest first.
func (s *Store) ListMedia() ([]Media, error) {
	rows, err := s.db.Query(
		`SELECT ` + mediaCols + ` FROM media ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list media: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Media
	for rows.Next() {
		m, err := scanMediaRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// --- scan helpers ---

// Column lists shared by every SELECT so a new column lands in exactly one
// place (a missed site would fail the scan-order contract at runtime).
const (
	subscriberCols = `id, email, status, confirm_token, COALESCE(confirm_expires_at,0), unsub_token, created_at, COALESCE(confirmed_at,0), updated_at`
	commentCols    = `id, post_slug, author_name, author_email, content, status, ip_hash, ua, created_at, COALESCE(moderated_at,0)`
	newsletterCols = `id, subject, markdown, html, post_url, status, sent_count, failed_count, created_at, COALESCE(sent_at,0)`
	mediaCols      = `id, key, url, bytes, content_type, created_at`
)

type rowScanner interface{ Scan(dest ...any) error }

func scanSubscriber(r rowScanner) (*Subscriber, error) {
	var sub Subscriber
	err := r.Scan(&sub.ID, &sub.Email, &sub.Status, &sub.ConfirmToken, &sub.ConfirmExpiresAt,
		&sub.UnsubToken, &sub.CreatedAt, &sub.ConfirmedAt, &sub.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan subscriber: %w", err)
	}
	return &sub, nil
}

func scanSubscriberRows(r rowScanner) (*Subscriber, error) { return scanSubscriber(r) }

func scanComment(r rowScanner) (*Comment, error) {
	var c Comment
	err := r.Scan(&c.ID, &c.PostSlug, &c.AuthorName, &c.AuthorEmail, &c.Content, &c.Status,
		&c.IPHash, &c.UA, &c.CreatedAt, &c.ModeratedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan comment: %w", err)
	}
	return &c, nil
}

func scanCommentRows(r rowScanner) (*Comment, error) { return scanComment(r) }

func scanNewsletter(r rowScanner) (*Newsletter, error) {
	var n Newsletter
	err := r.Scan(&n.ID, &n.Subject, &n.Markdown, &n.HTML, &n.PostURL, &n.Status,
		&n.SentCount, &n.FailedCount, &n.CreatedAt, &n.SentAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan newsletter: %w", err)
	}
	return &n, nil
}

func scanNewsletterRows(r rowScanner) (*Newsletter, error) { return scanNewsletter(r) }

func scanMedia(r rowScanner) (*Media, error) {
	var m Media
	err := r.Scan(&m.ID, &m.Key, &m.URL, &m.Bytes, &m.ContentType, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan media: %w", err)
	}
	return &m, nil
}

func scanMediaRows(r rowScanner) (*Media, error) { return scanMedia(r) }

// ── 富统计聚合（admin 统计页）：时间线 / 路径 / 来源 ──────────────────
// 日界用进程本地时区（部署侧以 TZ 环境变量对齐受众时区）。

// DayStat 是时间线上的一天：PV、UV（同日 ip_hash 去重）。
type DayStat struct {
	Day string // YYYY-MM-DD（本地时区）
	PV  int
	UV  int
}

// PageviewTimeseries returns per-day PV/UV since the unix timestamp, scoped to
// one site ("" = the blog). 零流量的天不出现（连续序列由展示层补零）。窗口
// 超出保留期时合并 daily_stats 永久层与未聚合的 raw 天：明细被清理的历史日
// 照常出柱子。raw 兜底按 (site, day) 对排除。
func (s *Store) PageviewTimeseries(site string, since int64) ([]DayStat, error) {
	if !s.useRollupStats(since) {
		rows, err := s.db.Query(
			`SELECT date(created_at,'unixepoch','localtime'), COUNT(*), COUNT(DISTINCT ip_hash)
			 FROM pageviews WHERE site=? AND created_at>=? GROUP BY 1 ORDER BY 1`, site, since)
		if err != nil {
			return nil, fmt.Errorf("pageview timeseries: %w", err)
		}
		defer func() { _ = rows.Close() }()
		var out []DayStat
		for rows.Next() {
			var d DayStat
			if err := rows.Scan(&d.Day, &d.PV, &d.UV); err != nil {
				return nil, err
			}
			out = append(out, d)
		}
		return out, rows.Err()
	}

	// 永久层：窗口内已聚合的天（可能明细已清）。
	rows, err := s.db.Query(
		`SELECT day, pv, uv FROM daily_stats
		 WHERE site=? AND day >= date(?,'unixepoch','localtime') ORDER BY day`, site, since)
	if err != nil {
		return nil, fmt.Errorf("pageview timeseries rollup: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]DayStat, 0, 64)
	for rows.Next() {
		var d DayStat
		if err := rows.Scan(&d.Day, &d.PV, &d.UV); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	// raw 兜底：daily_stats 缺位的天（含今天）。
	rows, err = s.db.Query(
		`SELECT date(created_at,'unixepoch','localtime'), COUNT(*), COUNT(DISTINCT ip_hash)
		 FROM pageviews
		 WHERE site=? AND created_at>=?
		   AND NOT EXISTS (SELECT 1 FROM daily_stats d
		                   WHERE d.site=pageviews.site
		                     AND d.day=date(pageviews.created_at,'unixepoch','localtime'))
		 GROUP BY 1 ORDER BY 1`, site, since)
	if err != nil {
		return nil, fmt.Errorf("pageview timeseries raw tail: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var d DayStat
		if err := rows.Scan(&d.Day, &d.PV, &d.UV); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	// 两个来源按 day 有序且互斥，整体仍有序。
	return out, rows.Err()
}

// HourStat 是时间线上的一小时：PV、UV（同小时 ip_hash 去重）。
type HourStat struct {
	Hour string // YYYY-MM-DD HH（本地时区）
	PV   int
	UV   int
}

// PageviewTimeseriesHourly returns per-hour PV/UV since the unix timestamp,
// scoped to one site ("" = the blog). 供 24 小时窗口的统计页使用（日粒度在该
// 窗口只剩一两根柱子）。
func (s *Store) PageviewTimeseriesHourly(site string, since int64) ([]HourStat, error) {
	rows, err := s.db.Query(
		`SELECT strftime('%Y-%m-%d %H', created_at,'unixepoch','localtime'), COUNT(*), COUNT(DISTINCT ip_hash)
		 FROM pageviews WHERE site=? AND created_at>=? GROUP BY 1 ORDER BY 1`, site, since)
	if err != nil {
		return nil, fmt.Errorf("pageview timeseries hourly: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []HourStat
	for rows.Next() {
		var h HourStat
		if err := rows.Scan(&h.Hour, &h.PV, &h.UV); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// PathStat 是单条路径的 PV/UV。
type PathStat struct {
	Path string
	PV   int
	UV   int
}

// PathStats returns top paths by PV since the unix timestamp, scoped to one
// site ("" = the blog).
func (s *Store) PathStats(site string, since int64, limit int) ([]PathStat, error) {
	rows, err := s.db.Query(
		`SELECT path, COUNT(*), COUNT(DISTINCT ip_hash) FROM pageviews
		 WHERE site=? AND created_at>=? GROUP BY path ORDER BY 2 DESC, path LIMIT ?`, site, since, limit)
	if err != nil {
		return nil, fmt.Errorf("path stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PathStat
	for rows.Next() {
		var p PathStat
		if err := rows.Scan(&p.Path, &p.PV, &p.UV); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RefStat 是单来源的 PV/UV；Referrer 空串 = 直接访问。
type RefStat struct {
	Referrer string
	PV       int
	UV       int
}

// ReferrerStats returns top referrers by PV since the unix timestamp, scoped
// to one site ("" = the blog).
func (s *Store) ReferrerStats(site string, since int64, limit int) ([]RefStat, error) {
	rows, err := s.db.Query(
		`SELECT referrer, COUNT(*), COUNT(DISTINCT ip_hash) FROM pageviews
		 WHERE site=? AND created_at>=? GROUP BY referrer ORDER BY 2 DESC, referrer LIMIT ?`, site, since, limit)
	if err != nil {
		return nil, fmt.Errorf("referrer stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RefStat
	for rows.Next() {
		var r RefStat
		if err := rows.Scan(&r.Referrer, &r.PV, &r.UV); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SiteStatRow 是多站总览的一行（管理台统计页）：今日/近 7 天/查询窗口的
// PV 与 UV。Site 空串 = 博客本站。
type SiteStatRow struct {
	Site             string
	TodayPV, TodayUV int
	WeekPV, WeekUV   int // 近 7 天（含今天）
	MonthPV, MonthUV int // 查询窗口全程（调用方传 30 天）
}

// SiteStats 聚合各站的三窗口 PV/UV，数据源与 PageviewStats 同一套合并口径
// （daily_stats 永久层 + (site,day) 对排除的 raw 兜底）。按窗口 PV 降序。
func (s *Store) SiteStats(since time.Time) ([]SiteStatRow, error) {
	sinceDay := since.Format("2006-01-02")
	today := s.now().Format("2006-01-02")
	week := s.now().AddDate(0, 0, -6).Format("2006-01-02")

	type dayRow struct {
		site, day string
		pv, uv    int
	}
	var days []dayRow

	rows, err := s.db.Query(
		`SELECT site, day, pv, uv FROM daily_stats WHERE day >= ?`, sinceDay)
	if err != nil {
		return nil, fmt.Errorf("site stats rollup: %w", err)
	}
	for rows.Next() {
		var d dayRow
		if err := rows.Scan(&d.site, &d.day, &d.pv, &d.uv); err != nil {
			rows.Close()
			return nil, err
		}
		days = append(days, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	_ = rows.Close()

	rows, err = s.db.Query(
		`SELECT site, date(created_at,'unixepoch','localtime'), COUNT(*), COUNT(DISTINCT ip_hash)
		 FROM pageviews
		 WHERE date(created_at,'unixepoch','localtime') >= ?
		   AND NOT EXISTS (SELECT 1 FROM daily_stats d
		                   WHERE d.site=pageviews.site
		                     AND d.day=date(pageviews.created_at,'unixepoch','localtime'))
		 GROUP BY 1, 2`, sinceDay)
	if err != nil {
		return nil, fmt.Errorf("site stats raw tail: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var d dayRow
		if err := rows.Scan(&d.site, &d.day, &d.pv, &d.uv); err != nil {
			return nil, err
		}
		days = append(days, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	bySite := map[string]*SiteStatRow{}
	for _, d := range days {
		r := bySite[d.site]
		if r == nil {
			r = &SiteStatRow{Site: d.site}
			bySite[d.site] = r
		}
		r.MonthPV += d.pv
		r.MonthUV += d.uv
		if d.day >= week {
			r.WeekPV += d.pv
			r.WeekUV += d.uv
		}
		if d.day >= today {
			r.TodayPV += d.pv
			r.TodayUV += d.uv
		}
	}
	out := make([]SiteStatRow, 0, len(bySite))
	for _, r := range bySite {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MonthPV != out[j].MonthPV {
			return out[i].MonthPV > out[j].MonthPV
		}
		return out[i].Site < out[j].Site
	})
	return out, nil
}

// DeleteNewsletter removes a newsletter. sending（在途群发）拒删；
// 删除连带清理 send_log（事务内完成，不留孤儿投递记录）。
func (s *Store) DeleteNewsletter(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("delete newsletter: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	if err := tx.QueryRow(`SELECT status FROM newsletters WHERE id=?`, id).Scan(&status); err != nil {
		return ErrNotFound
	}
	if status == "sending" {
		return fmt.Errorf("newsletter %d is sending", id)
	}
	if _, err := tx.Exec(`DELETE FROM send_log WHERE newsletter_id=?`, id); err != nil {
		return fmt.Errorf("delete send_log: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM newsletters WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete newsletter: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ── Settings：管理台持久化配置（播客分发等小数据）──────────────────

// GetSetting returns the value for key, or "" when unset.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get setting: %w", err)
	}
	return v, nil
}

// SetSetting upserts a setting value.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, s.now().Unix())
	if err != nil {
		return fmt.Errorf("set setting: %w", err)
	}
	return nil
}
