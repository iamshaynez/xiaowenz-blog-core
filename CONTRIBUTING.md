# Contributing

Thanks for your interest! This project welcomes issues and pull requests.

## Ground rules

[AGENTS.md](AGENTS.md) is the binding engineering constitution — it applies to humans and AI agents alike. The short version:

1. **Strict TDD**: failing test first, then minimal implementation, then refactor. Tests assert user-visible behavior and contracts, not implementation details.
2. **CI must stay green**: `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .`, `go build ./...`, and `docker build`.
3. **Database changes only via new goose migrations** (`internal/db/migrations/NNNN_description.sql`, additive, with a real `Down`).
4. **No secrets in the repo** — configuration is environment variables only; extend `.env.example` + both READMEs' config tables when you add one.

## Setup

```sh
git clone https://github.com/iamshaynez/xiaowenz-blog-core.git
cd xiaowenz-blog-core
cp .env.example .env
make test   # should be green before you change anything
```

Dev mode runs fully offline: emails print to the log, Turnstile is skipped, R2-less uploads degrade gracefully. The [manual smoke script](README.zh-CN.md#手工冒烟验收-83) exercises the full subscribe → verify → comment → moderate → campaign loop.

## Pull requests

- One concern per PR; describe the behavior change, not just the diff.
- Follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`).
- Every new behavior needs its failing-then-passing test; every bug fix needs a regression test.
- Update docs in the same PR: README.md (English) **and** README.zh-CN.md, `.env.example`, SPEC when the contract changes.
- Keep dependencies minimal — stdlib first; justify any new module in the PR description.

## Reporting bugs

Open an issue with: what you did, what you expected, what happened, and the redacted logs/config (never paste secrets). For security vulnerabilities, use [SECURITY.md](SECURITY.md) instead.
