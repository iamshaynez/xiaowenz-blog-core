## What & why

<!-- One paragraph: the behavior change and the user-visible reason. -->

## TDD checklist

- [ ] Failing test written first (link the commit or paste the RED output)
- [ ] `go test ./... -race -count=1` green
- [ ] `go vet ./...` and `gofmt -l .` clean
- [ ] Docs updated in both READMEs + `.env.example` if config changed
- [ ] Schema change? New goose migration with a real `Down` (never edits an applied one)
