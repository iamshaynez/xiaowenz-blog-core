# Security Policy

## Reporting a vulnerability

**Please do not open public issues for security vulnerabilities.**

Report them privately to: **xiaowen.z@outlook.com**

Include: affected version/commit, reproduction steps, impact assessment. You can expect an acknowledgment within 72 hours and a fix or mitigation plan within 14 days for confirmed issues.

## Scope

This service handles email addresses, comments, and admin credentials. We especially care about:

- Authentication/session flaws in `/admin/*` (HMAC cookies, token compare, rate limiting)
- Injection: SQL, email header (CRLF), HTML/XSS in admin-rendered pages or emails
- Token forgery for verify/unsub/moderation links
- Rate-limit bypass (including via forged `X-Forwarded-For`)
- Leakage of subscriber data or raw IP addresses

## Supported versions

| Version | Supported |
|---|---|
| latest `main` | ✅ |
| older releases | ❌ (single-maintainer project; please upgrade) |

## Design notes (for researchers)

- All SQL is parameterized; all HTML output goes through `html/template` escaping.
- IPs are stored only as `sha256(ip + UTC date)` — no raw IPs at rest.
- Admin login is rate-limited per IP and compares tokens in constant time.
- Public write endpoints require Turnstile (configurable) + honeypot + per-IP limits.
- `X-Forwarded-For` is honored only from `CORE_TRUSTED_PROXIES`.
