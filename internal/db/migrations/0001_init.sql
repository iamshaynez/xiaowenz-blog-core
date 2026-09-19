-- +goose Up
CREATE TABLE subscribers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','unsubscribed')),
    confirm_token TEXT NOT NULL DEFAULT '',
    confirm_expires_at INTEGER,
    unsub_token TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    confirmed_at INTEGER,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_subscribers_email ON subscribers(email);

CREATE TABLE comments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    post_slug TEXT NOT NULL,
    author_name TEXT NOT NULL,
    author_email TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    ip_hash TEXT NOT NULL DEFAULT '',
    ua TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    moderated_at INTEGER
);
CREATE INDEX idx_comments_post_status ON comments(post_slug, status);

CREATE TABLE newsletters (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subject TEXT NOT NULL,
    markdown TEXT NOT NULL,
    html TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','sending','sent','failed')),
    sent_count INTEGER NOT NULL DEFAULT 0,
    failed_count INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    sent_at INTEGER
);

CREATE TABLE downloads (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL,
    ip_hash TEXT NOT NULL DEFAULT '',
    ua TEXT NOT NULL DEFAULT '',
    referer TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_downloads_slug_created ON downloads(slug, created_at);

CREATE TABLE pageviews (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT NOT NULL,
    referrer TEXT NOT NULL DEFAULT '',
    ip_hash TEXT NOT NULL DEFAULT '',
    ua TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_pageviews_created ON pageviews(created_at);

CREATE TABLE media (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    url TEXT NOT NULL,
    bytes INTEGER NOT NULL DEFAULT 0,
    content_type TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE TABLE send_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    newsletter_id INTEGER NOT NULL REFERENCES newsletters(id),
    subscriber_id INTEGER NOT NULL REFERENCES subscribers(id),
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_send_log_created ON send_log(created_at);

-- +goose Down
DROP TABLE IF EXISTS send_log;
DROP TABLE IF EXISTS media;
DROP TABLE IF EXISTS pageviews;
DROP TABLE IF EXISTS downloads;
DROP TABLE IF EXISTS newsletters;
DROP TABLE IF EXISTS comments;
DROP TABLE IF EXISTS subscribers;
