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
	"strings"
	"time"
)

// Store wraps the SQL database. now is injectable for tests (clock fake).
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// New creates a Store over an already-migrated database.
func New(d *sql.DB) *Store {
	return &Store{db: d, now: time.Now}
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

// HashIP implements the privacy rule: sha256(ip + YYYY-MM-DD) hex, daily rotation.
func HashIP(ip string, t time.Time) string {
	sum := sha256.Sum256([]byte(ip + t.UTC().Format("2006-01-02")))
	return hex.EncodeToString(sum[:])
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
	Status      string
	SentCount   int
	FailedCount int
	CreatedAt   int64
	SentAt      int64
}

// CreateNewsletter stores a rendered draft.
func (s *Store) CreateNewsletter(subject, markdown, html string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO newsletters (subject, markdown, html, status, created_at)
		 VALUES (?, ?, ?, 'draft', ?)`, subject, markdown, html, s.now().Unix())
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

// MarkNewsletterSent records final counts and sent_at.
func (s *Store) MarkNewsletterSent(id int64, sent, failed int) error {
	res, err := s.db.Exec(
		`UPDATE newsletters SET status='sent', sent_count=?, failed_count=?, sent_at=? WHERE id=?`,
		sent, failed, s.now().Unix(), id)
	if err != nil {
		return fmt.Errorf("mark sent: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
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

// RecordPageview inserts one pageview row.
func (s *Store) RecordPageview(path, referrer, ipHash, ua string) error {
	_, err := s.db.Exec(
		`INSERT INTO pageviews (path, referrer, ip_hash, ua, created_at) VALUES (?, ?, ?, ?, ?)`,
		path, referrer, ipHash, ua, s.now().Unix())
	if err != nil {
		return fmt.Errorf("record pageview: %w", err)
	}
	return nil
}

// PageviewStats returns PV count and UV (distinct ip_hash) since epoch second.
func (s *Store) PageviewStats(since int64) (pv, uv int, err error) {
	err = s.db.QueryRow(
		`SELECT COUNT(*), COUNT(DISTINCT ip_hash) FROM pageviews WHERE created_at>=?`, since).
		Scan(&pv, &uv)
	if err != nil {
		return 0, 0, fmt.Errorf("pageview stats: %w", err)
	}
	return pv, uv, nil
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
	newsletterCols = `id, subject, markdown, html, status, sent_count, failed_count, created_at, COALESCE(sent_at,0)`
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
	err := r.Scan(&n.ID, &n.Subject, &n.Markdown, &n.HTML, &n.Status,
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

// PageviewTimeseries returns per-day PV/UV since the unix timestamp.
// 零流量的天不出现（连续序列由展示层补零）。
func (s *Store) PageviewTimeseries(since int64) ([]DayStat, error) {
	rows, err := s.db.Query(
		`SELECT date(created_at,'unixepoch','localtime'), COUNT(*), COUNT(DISTINCT ip_hash)
		 FROM pageviews WHERE created_at>=? GROUP BY 1 ORDER BY 1`, since)
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

// PathStat 是单条路径的 PV/UV。
type PathStat struct {
	Path string
	PV   int
	UV   int
}

// PathStats returns top paths by PV since the unix timestamp.
func (s *Store) PathStats(since int64, limit int) ([]PathStat, error) {
	rows, err := s.db.Query(
		`SELECT path, COUNT(*), COUNT(DISTINCT ip_hash) FROM pageviews
		 WHERE created_at>=? GROUP BY path ORDER BY 2 DESC, path LIMIT ?`, since, limit)
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

// ReferrerStats returns top referrers by PV since the unix timestamp.
func (s *Store) ReferrerStats(since int64, limit int) ([]RefStat, error) {
	rows, err := s.db.Query(
		`SELECT referrer, COUNT(*), COUNT(DISTINCT ip_hash) FROM pageviews
		 WHERE created_at>=? GROUP BY referrer ORDER BY 2 DESC, referrer LIMIT ?`, since, limit)
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
