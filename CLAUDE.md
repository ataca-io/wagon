# CLAUDE.md

wagon is the client CLI for rail. It speaks to rail's `/who` and `/api/v1/*` over mTLS, and uploads attachments straight to depot through an upload grant. It is a single stdlib-only `main` package plus `internal/buildinfo`.

```bash
make build | test | lint | fmt
go run . --server https://smtp.ataca.io --cert c.crt --key c.key whoami
```

- Pure Go, `CGO_ENABLED=0`, no third-party dependencies.
- The API contract lives in rail (`internal/httpd/openapi.yaml`). A rail API change that wagon uses lands there first.
- Go conventions follow rail's `CLAUDE.md` and `docs/code-style.md`: `errors.AsType`, `cmp.Or`, table-driven stdlib tests, comments of 2 to 3 lines at most.
- A `v*` tag runs `release.yml`; it needs green CI on the tagged commit.
- Before a push: `goimports -w . && golangci-lint run ./... && go test -race ./...`.
