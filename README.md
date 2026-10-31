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
wagon auth                               # show the active certificate and its expiry
```

`wagon auth setup` refuses a certificate and key that do not match. It writes both at mode 0600 under a 0700 `~/.wagon`.

To keep several certificates, add named identities and switch between them:

```bash
wagon auth add work work.crt work.key    # installs the identity "work"
wagon auth use work                      # makes "work" the active identity
wagon auth list                          # lists identities, marks the active one
wagon --identity default whoami          # use another identity for one call
```

`wagon auth setup` installs the identity named `default` in `~/.wagon`. Other identities live in `~/.wagon/identities/<name>/`. `~/.wagon/config.json` records the active identity. `wagon auth add` does not change it. Names are 1 to 64 characters: a lowercase letter or digit, then lowercase letters, digits, `.`, `_`, and `-`.

## Use

`--server` defaults to `https://smtp.ataca.io`, and the certificate pair defaults to the active identity in `~/.wagon`, so the rest needs no flags:

```bash
wagon whoami                                    # identity, allowed senders, webhooks, owned domains
wagon send --from app@ataca.io --to user@example.com \
  --subject "Your report" --text "It is ready."
wagon send --from app@ataca.io --to user@example.com \
  --subject "Your report" --body-file report.txt \
  --attach-file summary.pdf --attach-file export.csv
wagon send-file --to user@example.com summary.pdf export.csv
wagon test --to you@example.com                 # one-off test, sender from the cert
wagon messages list                             # your own messages, newest first
wagon messages list --status bounced --since 2026-09-01
wagon message 01jx...                           # details, attached files, deliveries, and opens
wagon messages deliveries 01jx...               # every attempt on a message
wagon messages opens 01jx...
wagon forms list
wagon forms create --name contact --from noreply@ataca.io \
  --to you@example.com --subject "Contact form" --redirect https://example.com/thanks
wagon --json whoami                             # identity JSON with an added domains array
wagon completion zsh                            # shell completion script, see below
```

- `messages list` takes `--status`, `--from`, `--to`, `--since`, `--until`, and `--limit`. While more rows exist, it prints the `--before <next id>` command for the next page.
- `messages list` shows each message's attachment count in the `ATT` column. `message <id>` lists the files by name, type, and size, with depot's scan verdict for an inbound part. rail records the files of API sends, inbound mail, and form submissions, not SMTP submissions.
- `--from` is optional on `send`, `send-file`, and `forms create`. It defaults to the certificate's first sender.
- A sender the certificate does not allow fails with the list of allowed senders.
- `--to`, `--cc`, and `--attach-file` repeat.
- `--attach-file` mints one upload grant, uploads each file, and sends the resulting ids as attachments. A send takes up to 10 files.
- `send-file` sends its file arguments the same way. Flags go before the files. The subject defaults to the file name, or `N files`. The body defaults to `Attached:` and the file names.
- `wagon test` uses the certificate's first email SAN as the sender.
- `whoami` lists all owned domains, following pagination. JSON output preserves identity fields and adds the complete domain records.

## Domains

Manage domains owned by your certificate's client identity:

```bash
wagon domains list --limit 20
wagon domains list --limit 20 --after CURSOR
wagon domains create example.com
wagon domains create example.net --dns-mode delegated
wagon domains get example.com
wagon domains records example.com
wagon domains verify example.com
wagon domains update example.com --webhook https://example.com/hooks/mail \
  --mailbox help@example.com --receiving-enabled=true
wagon domains update example.com --receiving-enabled=false
wagon domains update example.com --clear-webhooks --clear-mailboxes
wagon domains delete example.com
```

Put the domain before flags. Creation defaults to `records` mode and displays the ownership TXT record.
Delegated mode displays the assigned nameservers and requires server support.
Configure the returned DNS records before running `verify`.
Changing `--dns-mode` rotates the ownership proof and pauses the domain until verification succeeds.

`records` prints the expected records, then checks published TXT, MX, and NS values through your system DNS resolver.
Each result shows `match`, `missing`, `mismatch`, or `error`, with the observed values or lookup error.
Lookups have a five-second timeout. Results reflect the resolver's cache and may lag recent DNS changes.
TXT values must match exactly; hostname comparisons ignore case and trailing dots. MX priorities must match too.
This checks whether each expected value is published. It does not evaluate SPF rules or perform a complete DMARC audit.
The command does not change rail's ownership status; use `verify` for that.
JSON output retains `records` and adds a `validation` array. Failed checks still produce the report before returning an error.

`update` changes only supplied fields. Repeat `--webhook` and `--mailbox` to replace each existing list.
Use `--clear-webhooks` and `--clear-mailboxes` to send empty lists.
Write boolean values as `--receiving-enabled=false` or `--receiving-enabled=true`.
Receiving requires verified ownership and a webhook or enabled forwarding rule.

Manage forwarding rules under a domain:

```bash
wagon domains forwards list example.com --limit 20
wagon domains forwards list example.com --limit 20 --after CURSOR
wagon domains forwards create example.com help yavosh@gmail.com --from-munging auto
wagon domains forwards update example.com help yavosh@gmail.com --enabled=false
wagon domains forwards update example.com help yavosh@gmail.com --enabled=true
wagon domains forwards update example.com help yavosh@gmail.com --from-munging=""
wagon domains forwards delete example.com help yavosh@gmail.com
```

Put the domain, local part, and destination before flags.
`--from-munging` accepts `never`, `always`, `auto`, `arc`, or an empty value for the server default.
Re-enabling a rule can clear a hard-bounce suppression. The response reports any remaining suppression.

Both list commands accept `--limit` from 1 to 100, with a server default of 100.
Pass the returned `next_cursor` as `--after` for the next page.
All domain commands support global `--json`. Successful deletes return no body, so JSON mode prints nothing.
Deleting a domain also removes its forwarding rules. Rail rejects deletion while certificate references remain.

## Shell completion

`wagon completion <bash|zsh|fish>` prints a completion script. Install it once for your shell:

```bash
# bash (needs the bash-completion package)
wagon completion bash > ~/.local/share/bash-completion/completions/wagon

# zsh: write to any directory on $fpath, then start a new shell
wagon completion zsh > "${fpath[1]}/_wagon"

# fish
wagon completion fish > ~/.config/fish/completions/wagon.fish
```

Or load it from `~/.zshrc` at each shell start, as with `direnv` or `starship`. The line must come after `compinit`; with oh-my-zsh, after `source $ZSH/oh-my-zsh.sh`:

```zsh
command -v wagon >/dev/null && eval "$(wagon completion zsh)"
```

To try it in the current shell only:

```bash
source <(wagon completion zsh)      # zsh, after compinit
eval "$(wagon completion bash)"     # bash; bash 3.2 cannot source <(...)
wagon completion fish | source      # fish
```

Tab completes:

- Verbs, subcommands, and flags.
- `--status` values.
- `--dns-mode` and `--from-munging` values.
- `--from` values, from the email addresses in your certificate.
- Identity names, for `auth use` and `--identity`.
- File paths, for `--cert`, `--key`, `--ca`, `--body-file`, `--attach-file`, and the `send-file`, `auth setup`, and `auth add` arguments.

Completion works offline. It never calls rail, so it does not complete message ids or form tokens.
It completes a flag value only in the `--flag value` form, not `--flag=value`.

## Configuration

For `--server` and `--ca`, a flag overrides the environment, which overrides the default. The certificate pair comes from the first of these that is set:

1. `--cert` and `--key`
2. `RAIL_CERT` and `RAIL_KEY`
3. `--identity`
4. `WAGON_IDENTITY`
5. `~/.wagon/config.json`
6. `default`

| Flag | Environment | Default |
|---|---|---|
| `--server` | `RAIL_SERVER` | `https://smtp.ataca.io` |
| `--identity` | `WAGON_IDENTITY` | `~/.wagon/config.json`, else `default` |
| `--cert` | `RAIL_CERT` | the active identity's `client.crt` |
| `--key` | `RAIL_KEY` | the active identity's `client.key` |
| `--ca` | `RAIL_CA` | system roots |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Usage error |
| 2 | Auth failure: TLS handshake, `401`, or `403` |
| 3 | Any other `4xx` |
| 4 | A `5xx`, transport error, or DNS lookup failure |
| 5 | Expected DNS records are missing or mismatched |

## Development

```bash
make build     # CGO_ENABLED=0 build; `wagon version` prints the `git describe` stamp
make install   # install into GOBIN
make test      # go test -race ./...
make lint      # golangci-lint
```

A `v*` tag runs `release.yml`, which requires green CI on the tagged commit and publishes the four zips.
