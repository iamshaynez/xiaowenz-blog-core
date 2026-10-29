# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Full-codebase review pass (architecture, quality, bugs). All changes test-first.

### Added (console-managed system parameters + backup scheduling)

- **Dedicated「系统参数」(system settings) admin page** (`internal/sysconfig`):
  both R2 targets — media (public bucket) and backup (private bucket) — are
  configured in the console and stored in the settings table. Resolution is
  settings-first with per-field fallback to `CORE_R2_*` env vars for media
  (existing deployments keep working; console values override without a
  restart, clients rebuild per operation). The page shows each field's
  effective source (database / env / unset); secrets are write-only (blank
  keeps the stored value, never rendered back); malformed values are
  rejected. Backup R2 is console-only by design — the env vars point at the
  public media bucket, which must never receive snapshots.
- **Cron-scheduled backups to a console-configured private R2 bucket**
  (`robfig/cron/v3`, zero transitive deps, added for battle-tested 5-field
  cron parsing). The schedule defaults to `0 4 * * *` and the whole R2 target
  (endpoint, access key, secret, bucket) is managed in the admin console —
  stored in the settings table, no environment variables involved; the client
  is rebuilt per operation so console edits apply without a restart. Backups
  can be triggered manually; retention is the most recent 7 snapshot pairs
  (was: fixed 24h ticker, 30-day retention, media bucket). Each snapshot
  ships with a `.meta.json` config digest (schema version, cron, non-secret
  service config — secrets are recorded as presence flags only). The stored
  backup secret is never rendered back (blank field keeps the old value), and
  since credentials live in the DB they travel with its backups — the console
  warns that the backup bucket must stay private. The console also gained a
  backup page (history, next-run, "run now", "test R2 connection"
  write→list→delete probe); concurrent triggers return a busy message instead
  of stacking.

### Security

- **X-Forwarded-For spoofing**: `clientIP` now walks the honored XFF chain
  right-to-left, skipping trusted proxies, instead of taking the first entry —
  a client behind the trusted CDN could prepend fake hops and rotate IPs to
  evade every per-IP rate limit (subscribe/comments) and poison download
  dedupe / UV stats.
- **One-click comment moderation is disabled when `CORE_ADMIN_TOKEN` is
  empty**: the HMAC signing key would degrade to a publicly derivable
  constant, making forged approve/reject links trivial (mirrors the
  "empty token = admin disabled" rule).
- **`POST /api/unsub` caps its body at 4 KiB**: the endpoint is public and
  unauthenticated; an unbounded JSON fallback decoder buffered
  attacker-sized payloads in memory.
- **SVG uploads are rejected** (`image/svg` / `image/svg+xml`): SVG executes
  scripts on the media origin — a stored-XSS carrier excluded by name from
  the image whitelist.
- **Subscriber CSV export neutralizes formula injection**: cells starting
  with `=`, `+`, `-`, `@`, tab or CR are prefixed with a single quote; a
  legal address like `=HYPERLINK@x.com` would otherwise execute on open in
  spreadsheet apps.
- **Moderation links expire after 7 days**: the token is now
  `id.action.expiry` under the HMAC (previously valid forever; links issued
  before this change stop working on deploy).

### Added

- Admin logout (`POST /admin/logout`, nav button) expires the session cookie.
- Newsletters carry `List-Unsubscribe-Post: List-Unsubscribe=One-Click`
  (RFC 8058) alongside `List-Unsubscribe`, enabling Gmail-style one-click
  unsubscribe (the endpoint already accepts the RFC 8058 POST).

### Fixed

- Admin stats endpoints (JSON + HTML) and the subscriber list no longer
  swallow count-query errors; they now fail with 500 like every other query.

### Changed

- Comment `post` field is capped at 200 chars (was unbounded up to the 1 MiB
  body limit), keeping junk rows out of the moderation queue.

### Performance

- CSV subscriber import runs as a single transaction instead of one implicit
  commit per row (a 10 MB legacy CSV previously fsynced once per email).

## [0.1.0] - 2026-09-19

Initial public release. Built test-first under the engineering constitution in
[AGENTS.md](AGENTS.md) (106 behavioral tests, `-race` clean, zero-network test suite)
against the functional contract in [docs/SPEC.md](docs/SPEC.md); independently audited
before release.

### Added

- **Newsletter**: double opt-in subscription flow (verify → welcome), prefetch-safe
  one-click unsubscribe, `List-Unsubscribe` header, Markdown-to-HTML campaigns
  (goldmark) sent in the background with per-recipient unsubscribe links,
  pacing, send log, and crash-safe status recovery.
- **Subscriber management**: admin list with status filter/pagination, CSV import
  (legacy-platform compatible, validated) and export.
- **Comments**: moderated queue, admin notification email with HMAC-signed
  one-click approve/reject links, public approved-only JSON feed.
- **Podcast analytics**: `GET /a/{slug}.mp3` counts downloads with same-day
  per-listener dedupe, then 302-redirects to R2 (Podtrac-style).
- **Stats**: DNT-aware page-view beacon; admin dashboard (subscribers, pending
  comments, 30-day PV/UV, per-episode downloads).
- **Media**: admin uploads to Cloudflare R2 (S3) with MIME whitelist and
  collision-safe naming.
- **Persistence**: embedded SQLite (WAL) with goose auto-migrations; daily
  snapshot backups to R2 with 30-day retention.
- **Security baseline**: Turnstile + honeypot + per-IP rate limits on public
  write endpoints, HMAC session cookies, constant-time token compare, Origin
  checks on admin POSTs, CRLF-filtered email headers, SHA-256 daily-rotated IP
  storage, trusted-proxy-only XFF, request body caps and full server timeouts.
- **Ops**: multi-stage Dockerfile (non-root, `VOLUME /data`, healthcheck),
  graceful shutdown, `slog` structured logging, CI (test/vet/fmt/build/docker).
