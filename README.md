# wagon

`wagon` is the client CLI for [rail](https://smtp.ataca.io/agents), a transactional email server. It wraps rail's `/who` and `/api/v1/*` endpoints with your client certificate, so you never hand-roll an mTLS call.

## Install

```bash
go install github.com/ataca-io/wagon@latest
```

Or download a `wagon-<os>-<arch>.zip` from [Releases](https://github.com/ataca-io/wagon/releases). Builds exist for Linux and macOS, on amd64 and arm64.

## Set up

Point wagon at your rail client certificate once:

```bash
wagon auth setup my-app.crt my-app.key   # copies both into ~/.wagon, mode 0600
wagon auth                               # show the installed certificate and its expiry
```

`wagon auth setup` refuses a certificate and key that do not match. It writes both at mode 0600 under a 0700 `~/.wagon`.

## Use

`--server` defaults to `https://smtp.ataca.io`, and the certificate pair defaults to `~/.wagon`, so the rest needs no flags:

```bash
wagon whoami                                    # identity, allowed senders, webhooks
wagon send --from app@ataca.io --to user@example.com \
  --subject "Your report" --text "It is ready."
wagon send --from app@ataca.io --to user@example.com \
  --subject "Your report" --body-file report.txt \
  --attach-file summary.pdf --attach-file export.csv
wagon send-file --to user@example.com summary.pdf export.csv
wagon test --to you@example.com                 # one-off test, sender from the cert
wagon messages list                             # your own messages, newest first
wagon messages list --status bounced --since 2026-09-01
wagon messages deliveries 01jx...               # every attempt on a message
wagon messages opens 01jx...
wagon forms list
wagon forms create --name contact --from noreply@ataca.io \
  --to you@example.com --subject "Contact form" --redirect https://example.com/thanks
wagon --json whoami                             # raw JSON, for any verb
```

- `messages list` takes `--status`, `--from`, `--to`, `--since`, `--until`, and `--limit`. While more rows exist, it prints the `--before <next id>` command for the next page.
- `--from` is optional on `send`, `send-file`, and `forms create`. It defaults to the certificate's first sender.
- A sender the certificate does not allow fails with the list of allowed senders.
- `--to`, `--cc`, and `--attach-file` repeat.
- `--attach-file` mints one upload grant, uploads each file, and sends the resulting ids as attachments. A send takes up to 10 files.
- `send-file` sends its file arguments the same way. Flags go before the files. The subject defaults to the file name, or `N files`. The body defaults to `Attached:` and the file names.
- `wagon test` uses the certificate's first email SAN as the sender.

## Configuration

Flags override the environment, which overrides `~/.wagon`:

| Flag | Environment | Default |
|---|---|---|
| `--server` | `RAIL_SERVER` | `https://smtp.ataca.io` |
| `--cert` | `RAIL_CERT` | `~/.wagon/client.crt` |
| `--key` | `RAIL_KEY` | `~/.wagon/client.key` |
| `--ca` | `RAIL_CA` | system roots |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Usage error |
| 2 | Auth failure: TLS handshake, `401`, or `403` |
| 3 | Any other `4xx` |
| 4 | A `5xx` or a transport error |

## Development

```bash
make build     # CGO_ENABLED=0 build; `wagon version` prints the `git describe` stamp
make install   # install into GOBIN
make test      # go test -race ./...
make lint      # golangci-lint
```

A `v*` tag runs `release.yml`, which requires green CI on the tagged commit and publishes the four zips.
