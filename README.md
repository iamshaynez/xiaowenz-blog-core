# xiaowenz-blog-core

[![CI](https://github.com/iamshaynez/xiaowenz-blog-core/actions/workflows/ci.yml/badge.svg)](https://github.com/iamshaynez/xiaowenz-blog-core/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/iamshaynez/xiaowenz-blog-core)](https://goreportcard.com/report/github.com/iamshaynez/xiaowenz-blog-core)

**English** | [简体中文](README.zh-CN.md)

A self-hosted companion service that gives a static site (Hugo, Astro, Jekyll — anything) the dynamic features of a hosted platform like Typlog/Substack:

- 📮 **Newsletter** — double opt-in subscriptions, list management, Markdown-to-email campaigns with per-recipient unsubscribe links
- 💬 **Comments** — moderated by default, one-click approve/reject links in the notification email, no third-party comment SaaS
- 🎙️ **Podcast analytics** — download counting via a Podtrac-style 302 redirect endpoint, same-day dedupe per listener
- 🖼️ **Media hosting** — admin uploads straight to Cloudflare R2 (zero egress fees)
- 📊 **Privacy-friendly stats** — page views without cookies; IPs are only stored as daily-rotated SHA-256 hashes
- ⚙️ **Console-managed storage** — media & backup R2 targets configured on the admin「系统参数」page (settings-first, `CORE_R2_*` env as per-field fallback for media); uploads and backups rebuild their clients per operation, so changes apply without a restart
- 💾 **Zero-ops persistence** — embedded SQLite (WAL) with automatic migrations and scheduled backups to a private R2 bucket (cron editable in the console; last 7 kept; manual run + connection test)

Single binary, single container, single volume. No framework, no CGO, no external database.

## Why

Hosted platforms bundle blog + podcast + newsletter and charge ~$200+/year, lock in your subscriber list, and cap how much you can modify. This project is the "dynamic half" of an escape hatch: keep your static site generator and your Git-based workflow, and add just the stateful features — owned by you, on a $5 VPS or a PaaS free tier.

It powers [xiaowenz.com](https://xiaowenz.com) in production.

## Architecture

```
static site (Hugo/Pages) ──JS──▶ xiaowenz-blog-core (Go, single binary)
  Hugo/Astro/...                    ├─ /api/*  subscribe, verify, unsub, comments, pv
  markdown in Git                   ├─ /a/{slug}.mp3 → count → 302 → R2
                                  ├─ /admin  server-rendered console
                                  ├─ SQLite (WAL, goose migrations) on /data volume
                                  ├─ SMTP (465/587) for all outbound mail
                                  └─ cron-scheduled backup → private R2 bucket (7 kept)
```

- Go 1.26, stdlib `net/http` only (ServeMux pattern routing) — no web framework
- SQLite via `modernc.org/sqlite` (pure Go, no CGO) — static Docker image
- All externalities behind interfaces (Mailer / Turnstile / S3 / clock): **124 behavioral tests, zero network access**, `-race` clean
- TDD by constitution — see [AGENTS.md](AGENTS.md) and the functional contract [docs/SPEC.md](docs/SPEC.md)

## Quickstart

### Docker (recommended)

```sh
docker build -t xiaowenz-blog-core .
docker run -d --name core \
  -p 8080:8080 -v core-data:/data \
  -e CORE_ADMIN_TOKEN='change-me' \
  -e CORE_BASE_URL='http://localhost:8080' \
  -e CORE_SITE_URL='http://localhost:1313' \
  xiaowenz-blog-core
curl localhost:8080/healthz   # {"db":true,"ok":true}
```

### From source

```sh
cp .env.example .env   # edit as needed
make test              # go test ./... -race -count=1
make run               # CORE_DB_PATH=/tmp/core.db go run ./cmd/core
```

**Dev mode is fully usable offline**: with no SMTP configured, all emails (verification links included) are printed to the log; Turnstile is skipped with a warning; R2-less uploads return a graceful 503.

Walk through the whole loop — subscribe → verify → comment → moderate → campaign → unsubscribe — with the [manual smoke script](README.zh-CN.md#手工冒烟验收-83).

## Configuration

Everything is an environment variable ([.env.example](.env.example)):

| Variable | Default | Description |
|---|---|---|
| `CORE_ADDR` | `:8080` | Listen address |
| `CORE_DB_PATH` | `/data/core.db` | SQLite file (volume mount point) |
| `CORE_BASE_URL` | `http://localhost:8080` | Public URL of this service (used in email links) |
| `CORE_SITE_URL` | `http://localhost:1313` | Your static site — the only CORS origin + redirect target |
| `CORE_ADMIN_TOKEN` | empty | Admin password; empty = admin login disabled |
| `CORE_SMTP_HOST/PORT/USER/PASS` | — | Outbound mail. 465 = TLS, 587 = STARTTLS. Empty = dev log mailer |
| `CORE_SMTP_FROM` | — | Sender, e.g. `My Blog <letter@example.com>`; also receives comment notifications |
| `CORE_R2_ENDPOINT/KEY_ID/SECRET/BUCKET/PUBLIC_BASE` | — | Media-bucket fallback only: the admin console「系统参数」page (settings table) wins field by field; backup R2 is console-only. Incomplete effective media config disables uploads with a warning |
| `CORE_TURNSTILE_SECRET` | — | Cloudflare Turnstile; empty = dev skip with warning |
| `CORE_TRUSTED_PROXIES` | empty | CIDRs whose `X-Forwarded-For` is trusted. Empty = never trust XFF |
| `CORE_ENV` | `dev` | `prod` enforces required vars + Secure cookies |
| `CORE_SEND_INTERVAL_MS` | `1000` | Newsletter send pacing (ms per recipient) |
| `CORE_MAILING_ADDRESS` | — | Physical address placeholder in the newsletter footer |

## Public API

| Endpoint | Purpose |
|---|---|
| `POST /api/subscribe` | Double opt-in: stores pending subscriber, sends verification mail (5/h/IP, Turnstile + honeypot) |
| `GET /api/verify?token=` | Activates subscription, sends welcome mail, redirects back to the site |
| `GET/POST /api/unsub` | Prefetch-safe unsubscribe (GET shows a confirm page, POST executes) |
| `POST /api/comments` | Submits a comment into the moderation queue, notifies the admin with one-click links |
| `GET /api/comments?post=` | Approved comments for a post (JSON, no email leakage) |
| `POST /api/pv` | Page-view beacon (DNT-aware) |
| `GET /a/{slug}.mp3` | Counts a podcast download (same-day per-IP dedupe), 302 → R2 |
| `GET /healthz` | Liveness/readiness probe |

The admin console at `/admin` (server-rendered, no JS framework) covers: dashboard, subscriber list/import/export, comment moderation, Markdown newsletter composer with preview & background sending, media uploads, and stats.

## Deployment

Any container host works. On [Zeabur](https://zeabur.com): import the repo (Dockerfile auto-detected), **mount a persistent volume at `/data`**, set the env vars with `CORE_ENV=prod`, bind your domain. See the [deployment notes](README.zh-CN.md#部署zeabur) for the full checklist.

**Hooking up a Hugo site** (config params, frontend JS contract, podcast redirect, acceptance checklist): [docs/HUGO.md](docs/HUGO.md) (中文).

## Development

```sh
make test / vet / fmt / build / run / docker
```

Contributions welcome — read [CONTRIBUTING.md](CONTRIBUTING.md) and the non-negotiable TDD rules in [AGENTS.md](AGENTS.md). Database schema only evolves through new goose migrations (`internal/db/migrations/`); every change ships with a `Down` migration and behavioral tests.

Security issues: please see [SECURITY.md](SECURITY.md) — do **not** open public issues for vulnerabilities.

## License

[MIT](LICENSE) © 2026 Xiaowen Zhang
