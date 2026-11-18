# wagon

`wagon` is the command-line interface (CLI) for [rail](https://smtp.ataca.io/agents), a transactional email server.
It uses your client certificate to authenticate requests with mutual TLS (mTLS).
It also uploads attachments and manages files in depot.

## Install wagon

Prebuilt ZIP archives are available for these platforms:

| Operating system | Architecture |
|---|---|
| Linux | amd64, arm64 |
| macOS | arm64 (Apple silicon) |
| Windows | amd64 (x64) |

### Linux and macOS

Before you run the installer, install `curl`, `unzip`, and either `sha256sum` or `shasum`.
To install the latest release, run:

```sh
curl -fsSL https://github.com/ataca-io/wagon/releases/latest/download/install.sh | sh
```

The installer verifies the ZIP archive's SHA-256 checksum and installs `wagon` into `~/.local/bin` without `sudo`.
If needed, it prints a command to add that directory to `PATH`.
To upgrade to the latest release, run the same command again.

### Windows

1. [Download the latest Windows x64 ZIP archive](https://github.com/ataca-io/wagon/releases/latest/download/wagon-windows-amd64.zip).
2. Extract `wagon.exe` into a directory on your `PATH`.

For other prebuilt archives, see [wagon releases](https://github.com/ataca-io/wagon/releases).

### Install with Go

With Go 1.27 or later installed, run:

```sh
go install github.com/ataca-io/wagon@latest
```

## Set up authentication

Before you start, obtain a rail client certificate and its matching private key.

1. Install the certificate and private key:

   ```sh
   wagon auth setup CERT_FILE KEY_FILE
   ```

   Replace *`CERT_FILE`* and *`KEY_FILE`* with the paths to your certificate and private key.
   The command rejects a certificate and private key that do not match.
   On Unix systems, it copies both files with `0600` permissions into `~/.wagon`, created with `0700` permissions.

2. Inspect the active certificate and its expiration date:

   ```sh
   wagon auth
   ```

3. Check your server identity, allowed senders, webhooks, and owned domains:

   ```sh
   wagon whoami
   ```

By default, wagon connects to `https://smtp.ataca.io` and uses the active identity in `~/.wagon`.
`whoami` retrieves all pages of owned domains.

### Manage multiple identities

To add and select a named identity:

1. Add the identity:

   ```sh
   wagon auth add work CERT_FILE KEY_FILE
   ```

   Replace *`CERT_FILE`* and *`KEY_FILE`* with the certificate and private key paths for the `work` identity.
   `auth add` installs the identity without changing the active identity.

2. Select the active identity:

   ```sh
   wagon auth use work
   ```

3. List identities and check which identity is active:

   ```sh
   wagon auth list
   ```

To select an identity for one command, use the global `--identity` flag:

```sh
wagon --identity default whoami
```

`auth setup` installs the `default` identity in `~/.wagon`.
Other identities use `~/.wagon/identities/IDENTITY_NAME/`.
Replace *`IDENTITY_NAME`* with the identity name.
`~/.wagon/config.json` records the active identity.

Identity names must contain 1 to 64 characters.
Start with a lowercase letter or digit.
For subsequent characters, use lowercase letters, digits, `.`, `_`, or `-`.

### Renew a rail certificate

To stage a renewed certificate and key for the active identity, run:

```sh
wagon auth renew --output-dir renewed
```

Use a directory name that does not exist. A valid certificate renews over mTLS with a new key.
Within rail's expiry grace period, wagon uses the existing key to recover an expired certificate.
The command leaves your current credential files in place.
After renewal, install `renewed/client.crt` and `renewed/client.key` with `auth setup` or `auth add`.

## Send email

To send a text message, run:

```sh
wagon send --to user@example.com \
  --subject "Your report" --text "It is ready."
```

By default, `send`, `send-file`, and `forms create` use the first sender address in your certificate.
To select another authorized sender, add `--from SENDER_EMAIL`.
Replace *`SENDER_EMAIL`* with an address permitted by your certificate.
If the certificate does not allow the sender, wagon reports the allowed addresses.

To read the body from a file and include attachments, run:

```sh
wagon send --to user@example.com \
  --subject "Your report" --body-file report.txt \
  --attach-file summary.pdf --attach-file export.csv
```

Repeat `--to`, `--cc`, and `--attach-file` to supply multiple values.
`--attach-file` uses rail upload grants in batches of up to 10 files.
With an explicit `--request-id`, wagon saves completed uploads in the local user cache.
To retry a send, reuse that ID and the same attachment files.

Use `--attachment-id FILE_ID` for an existing file uploaded with a rail-issued grant.
Use `--inline-file FILE` to send a local file through rail if inline attachments are enabled.
Repeat either flag to add multiple files.
A message can include up to 20 attachments across all three flags.

To send files with a generated subject and body, run:

```sh
wagon send-file --to user@example.com summary.pdf export.csv
```

For `send-file`, put flags before file arguments.
The subject defaults to the file name for one file, or `N files` for multiple files.
The body defaults to `Attached:` followed by the file names.
Add `--inline` to upload the files through rail.

To send a test message using the certificate's first sender address, run:

```sh
wagon test --to user@example.com
```

## Manage depot files

Set `--server` to the depot HTTPS URL on every command.
Use a client certificate that depot accepts.
Pass `--cert` and `--key`, or select a depot identity with `--identity`.

```sh
wagon --server DEPOT_URL depot upload --ttl 24h --exportable report.pdf
wagon --server DEPOT_URL depot upload --resumable report.pdf
wagon --server DEPOT_URL depot upload --resume FILE_ID report.pdf
```

A resumable upload prints the file ID to stderr.
To resume, use the printed ID and the same local file.
For uploads started by wagon, it verifies the file contents before resuming.
Depot fixes the retention, content type, and exportability when it creates the upload.

| Command after `wagon --server DEPOT_URL depot` | Action |
| --- | --- |
| `list [--before ID]` | List files. Use `next_before` to retrieve the next page. |
| `get FILE_ID` | Show file metadata, scan verdict, and any download link. |
| `progress FILE_ID` | Show the number of bytes depot has received. |
| `download FILE_ID` | Stream the file to stdout. |
| `claim FILE_ID [--ttl DURATION]` | Extend the file's retention. |
| `delete FILE_ID` | Delete the file. |
| `grant --ttl 10m --max-files 1 --max-bytes 1048576 --origin https://example.com` | Create a browser upload grant, if enabled on depot. |

A depot certificate scopes uploads and grants to that identity. Use a rail-issued grant for rail attachments.

## Inspect messages

To list your messages, starting with the newest, run:

```sh
wagon messages list
wagon messages list --status bounced --since 2026-09-01
```

Filter messages with `--status`, `--from`, `--to`, `--since`, and `--until`.
Use `--limit` to set the page size.
If more messages exist, wagon prints a command with `--before` to retrieve the next page.
The `ATT` column shows the attachment count.

To inspect a message, its delivery attempts, or its opens, run:

```sh
wagon message MESSAGE_ID
wagon messages deliveries MESSAGE_ID
wagon messages opens MESSAGE_ID
```

Replace *`MESSAGE_ID`* with an ID from `wagon messages list`.
`wagon message` includes file names, types, sizes, and depot scan results for inbound attachments.
The rail server records attachments for API messages, inbound messages, and form submissions.
It does not record attachments for SMTP submissions.

## Manage forms

To list forms or create a contact form, run:

```sh
wagon forms list
wagon forms create --name contact --to user@example.com \
  --subject "Contact form" --redirect https://example.com/thanks
```

## Check email addresses and verify ownership

These commands require rail's `email_checks` feature.
To check an address, run:

```sh
wagon emails check user@example.com
```

The response reports routing, domain category, SMTP results, and delivery evidence.
An HTTP `200` response produces exit code `0`, including assessments with `invalid`, `undeliverable`, or `unknown` status.
For `unknown`/`recipient_accepted` with SMTP result `accepted`, human output adds an `assessment` row after `reason`:

```text
SMTP accepted the recipient; mailbox existence is unconfirmed.
```

The status remains `unknown` because SMTP acceptance does not prove mailbox existence or ownership.
Catch-all servers can accept nonexistent recipients.
JSON output preserves the complete server response without adding this explanation.

To verify mailbox ownership:

1. Send an ownership verification code:

   ```sh
   wagon email-verifications create --email user@example.com \
     --purpose signup --subject-id account-123 --request-id signup-123
   ```

   Replace `signup`, `account-123`, and `signup-123` with your application's purpose, subject ID, and request ID.
   `--from` defaults to the certificate's first sender address.
   If you omit `--request-id`, wagon generates a request ID.

2. Check the verification status:

   ```sh
   wagon email-verifications get VERIFICATION_ID
   ```

   Replace *`VERIFICATION_ID`* with the `id` returned by `create`.
   The status is `pending`, `verified`, `expired`, or `locked`.

3. Confirm the code received by email:

   ```sh
   wagon email-verifications confirm VERIFICATION_ID --code VERIFICATION_CODE
   ```

   Replace *`VERIFICATION_CODE`* with the six-digit code, including any leading zeros.
   Confirmation consumes the code.
   Codes expire after 10 minutes and allow five failed attempts.

Verification applies to the authenticated client, email address, purpose, and application subject.
To retry without sending another message, reuse the accepted request ID.
To resend, use a new request ID with the same email address, sender, purpose, and subject ID.
Check `resend_at` before resending.
Resending preserves the expiration time and failed-attempt limit.

## Manage domains

Domain commands manage domains owned by your certificate's client identity.
Put positional arguments before command flags.

### Create and verify a domain

1. Create a domain:

   ```sh
   wagon domains create example.com
   ```

   Replace `example.com` with your domain.
   Creation defaults to `records` mode and displays the ownership TXT record.
   For delegated DNS mode, use `wagon domains create example.com --dns-mode delegated`.
   Delegated mode displays the assigned nameservers and requires server support.

2. Publish the returned DNS records with your DNS provider.
3. Verify domain ownership:

   ```sh
   wagon domains verify example.com
   ```

To inspect a domain or list owned domains, run:

```sh
wagon domains get example.com
wagon domains list --limit 20
```

`whoami`, `domains list`, and `domains get` show each domain's usage: Sending only or Sending and receiving.
Usage comes from `receiving_enabled`.
Ownership status and certificate sender permissions remain separate.
Domains that only send email can keep their externally hosted mailboxes and existing MX records.

### Check DNS records

The rail server must support `POST /api/v1/domains/{domain}/check`.
If the endpoint is unavailable, upgrade rail before using this command.
To display DNS guidance and validate published records, run:

```sh
wagon domains records example.com
```

The response includes each record's purpose and requirement.
For domains that only send email, rail omits MX guidance and skips the MX check.
Merge SPF guidance into your existing SPF record to retain other sending providers.
Keep a valid existing DMARC policy and report destination; the suggested value is an example.

The rail server evaluates SPF against its configured mail host IP addresses, falling back to mail host DNS.
It compares published DKIM keys with its stored public keys.
Delegated ownership checks use parent nameserver referrals.

Results show `pass`, `warn`, `fail`, `skip`, or `error`, with observed values and diagnostic details.
Warnings and skipped checks do not fail the command.
DNS errors produce an incomplete result.
Results reflect rail's resolver cache and might lag recent DNS changes.
This command does not change ownership status.
To change ownership status, run `wagon domains verify example.com`.

With `--json`, the response preserves the records response and adds a `validation` object.
The object contains domain metadata, `checked_at`, and `results` from the server.
For scripts that used the earlier local checks, `validation` is now an object instead of an array.
Failed checks include the report and `validation_error`.
If the check request fails, `validation` is `null` and DNS guidance remains available.

### Update or delete a domain

To configure receiving and delivery destinations, run:

```sh
wagon domains update example.com --webhook https://example.com/hooks/mail \
  --mailbox help@example.com --receiving-enabled=true
```

Receiving requires verified ownership and a webhook or enabled forwarding rule.
`update` changes only supplied fields.
Repeat `--webhook` or `--mailbox` to replace the corresponding list.
To send empty lists, use `--clear-webhooks` or `--clear-mailboxes`.
Write boolean values as `--receiving-enabled=false` or `--receiving-enabled=true`.

To disable receiving or clear delivery settings, run:

```sh
wagon domains update example.com --receiving-enabled=false
wagon domains update example.com --clear-webhooks --clear-mailboxes
```

Changing `--dns-mode` rotates the ownership proof and pauses the domain until verification succeeds.
Deleting a domain also removes its forwarding rules.
The rail server rejects deletion while certificate references remain.
To delete a domain, run:

```sh
wagon domains delete example.com
```

### Manage forwarding rules

Put the domain, local part, and destination before command flags.
To list, create, update, or delete forwarding rules, run:

```sh
wagon domains forwards list example.com --limit 20
wagon domains forwards create example.com help user@example.net \
  --from-munging auto
wagon domains forwards update example.com help user@example.net --enabled=false
wagon domains forwards update example.com help user@example.net --enabled=true
wagon domains forwards update example.com help user@example.net --from-munging=""
wagon domains forwards delete example.com help user@example.net
```

Replace `user@example.net` with the forwarding destination.
`--from-munging` accepts `never`, `always`, `auto`, `arc`, or an empty value for the server default.
Enabling a rule can clear a suppression caused by a hard bounce.
The response reports any remaining suppression.

### Retrieve additional pages

`domains list` and `domains forwards list` accept `--limit` values from 1 to 100.
The server default is 100.
To retrieve the next page, pass the returned `next_cursor` as `--after`:

```sh
wagon domains list --limit 20 --after CURSOR
wagon domains forwards list example.com --limit 20 --after CURSOR
```

Replace *`CURSOR`* with the `next_cursor` from the corresponding list response.

## Configure shell completion

`wagon` provides completion scripts for Bash, Zsh, and fish.
Completion supports commands, subcommands, and flags, including these values:

- Message statuses for `--status`.
- DNS modes for `--dns-mode` and rewrite modes for `--from-munging`.
- Sender addresses from your certificate for `--from`.
- Identity names for `auth use` and `--identity`.
- Paths for `--cert`, `--key`, `--ca`, `--body-file`, `--attach-file`, `--inline-file`, and `--output-dir`.
- File arguments for `send-file`, `depot upload`, `auth setup`, and `auth add`.

Completion works offline and does not retrieve message IDs or form tokens from rail.
For flag value completion, use `--flag value` instead of `--flag=value`.

### Bash

Install the `bash-completion` package before installing the completion script.
To install the script, run:

```bash
mkdir -p ~/.local/share/bash-completion/completions
wagon completion bash > ~/.local/share/bash-completion/completions/wagon
```

To load completion in the current shell, run:

```bash
eval "$(wagon completion bash)"
```

### Zsh

After initializing completion with `compinit`, choose one of these methods:

- Install the script into a writable directory on `$fpath`, then start a new shell:

  ```zsh
  wagon completion zsh > "${fpath[1]}/_wagon"
  ```

- Load completion at shell startup by adding this line to `~/.zshrc` after `compinit`:

  ```zsh
  command -v wagon >/dev/null && eval "$(wagon completion zsh)"
  ```

  With Oh My Zsh, put the line after `source $ZSH/oh-my-zsh.sh`.

To load completion in the current shell, run:

```zsh
source <(wagon completion zsh)
```

### fish

To install the completion script, run:

```fish
mkdir -p ~/.config/fish/completions
wagon completion fish > ~/.config/fish/completions/wagon.fish
```

To load completion in the current shell, run:

```fish
wagon completion fish | source
```

## Configuration

Global flags go before the command.
For `--server` and `--ca`, a flag overrides the environment variable, which overrides the default.
`wagon` resolves the certificate and private key from these sources, in order:

1. `--cert` and `--key`.
2. `RAIL_CERT` and `RAIL_KEY`.
3. `--identity`.
4. `WAGON_IDENTITY`.
5. The active identity in `~/.wagon/config.json`.
6. The `default` identity.

| Flag | Environment variable | Default |
|---|---|---|
| `--server` | `RAIL_SERVER` | `https://smtp.ataca.io` |
| `--identity` | `WAGON_IDENTITY` | Active identity in `~/.wagon/config.json`, or `default` |
| `--cert` | `RAIL_CERT` | Active identity's `client.crt` |
| `--key` | `RAIL_KEY` | Active identity's `client.key` |
| `--ca` | `RAIL_CA` | System certificate pool |

### Print JSON output

To print JSON, put the global `--json` flag before the command:

```sh
wagon --json whoami
wagon --json emails check user@example.com
```

`whoami` preserves identity fields and adds a `domains` array containing all owned domain records.
Email checks and ownership verification preserve the complete server response.
Domain commands also support `--json`.
Successful domain and forwarding rule deletions return no body, so JSON mode prints nothing.

## Exit codes

Use these exit codes to handle command results in scripts:

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Usage error |
| `2` | Authentication failure: TLS handshake, HTTP `401`, or HTTP `403` |
| `3` | Any other HTTP `4xx` response |
| `4` | HTTP `5xx`, transport error, or incomplete DNS validation |
| `5` | DNS validation reported a configuration failure |

## Develop wagon

With Go 1.27 or later installed, use these development commands:

| Command | Action |
|---|---|
| `make build` | Build with `CGO_ENABLED=0` and version information from `git describe`. |
| `make install` | Install into `GOBIN`, or `$(go env GOPATH)/bin` if `GOBIN` is unset. |
| `make test` | Run `go test -race ./...`. |
| `make lint` | Run `golangci-lint`. |

To inspect the build version, run `wagon version`.
A `v*` tag starts `release.yml`, which requires green CI on the tagged commit.
The workflow publishes four ZIP archives, `install.sh`, and `SHA256SUMS`.
