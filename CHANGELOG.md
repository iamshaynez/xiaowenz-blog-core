# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
