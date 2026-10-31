package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type domainResponse struct {
	Domain           string           `json:"domain"`
	Status           string           `json:"status"`
	DNSMode          string           `json:"dns_mode"`
	ReceivingEnabled bool             `json:"receiving_enabled"`
	WebhookURLs      []string         `json:"webhook_urls"`
	Mailboxes        []string         `json:"mailboxes"`
	OwnershipRecord  *domainDNSRecord `json:"ownership_record,omitempty"`
	Nameservers      []string         `json:"nameservers,omitempty"`
	VerifiedAt       string           `json:"verified_at"`
	LastCheckedAt    string           `json:"last_checked_at"`
	CreatedAt        string           `json:"created_at"`
	UpdatedAt        string           `json:"updated_at"`
}

type domainDNSRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

func runDomains(c *client, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return &usageError{"usage: domains <list|get|create|update|delete|records|verify|forwards> [args]"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runDomainsList(c, rest, out, stderr)
	case "create":
		return runDomainsCreate(c, rest, out, stderr)
	case "update":
		return runDomainsUpdate(c, rest, out, stderr)
	case "get", "delete", "records", "verify":
		return runDomainAction(c, sub, rest, out, stderr)
	case "forwards":
		return runDomainForwards(c, rest, out, stderr)
	default:
		return &usageError{fmt.Sprintf("unknown domains subcommand %q", sub)}
	}
}

// parseDomainFlags accepts the named positional arguments before flags.
// Extra arguments are rejected, including space-separated boolean values.
func parseDomainFlags(fs *flag.FlagSet, args []string, names ...string) error {
	fs.Usage = func() {
		_, _ = fmt.Fprintf(fs.Output(), "Usage: %s %s [flags]\n", fs.Name(), strings.Join(names, " "))
		fs.PrintDefaults()
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return fs.Parse(args)
	}
	if len(args) < len(names) {
		return &usageError{fmt.Sprintf("usage: %s %s [flags]", fs.Name(), strings.Join(names, " "))}
	}
	for i, name := range names {
		if args[i] == "" || (i == 0 && strings.HasPrefix(args[i], "-")) {
			return &usageError{fmt.Sprintf("%s requires %s before flags", fs.Name(), name)}
		}
	}
	if err := fs.Parse(args[len(names):]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}
	if fs.NArg() != 0 {
		return &usageError{fmt.Sprintf("%s takes %d positional arguments before flags; write boolean flags as --flag=false", fs.Name(), len(names))}
	}
	return nil
}

func domainPath(domain string) string {
	return "/api/v1/domains/" + url.PathEscape(domain)
}

// domainPageQuery preserves opaque cursors and validates explicit page sizes.
func domainPageQuery(fs *flag.FlagSet, limit int, after string) (string, error) {
	q := url.Values{}
	if visitedFlags(fs)["limit"] {
		if limit < 1 || limit > 100 {
			return "", &usageError{"--limit must be between 1 and 100"}
		}
		q.Set("limit", strconv.Itoa(limit))
	}
	if after != "" {
		q.Set("after", after)
	}
	if len(q) == 0 {
		return "", nil
	}
	return "?" + q.Encode(), nil
}

func runDomainsList(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("domains list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("limit", 100, "page size (1-100)")
	after := fs.String("after", "", "cursor from the previous page's next_cursor")
	if err := parseDomainFlags(fs, args); err != nil {
		return err
	}
	query, err := domainPageQuery(fs, *limit, *after)
	if err != nil {
		return err
	}
	var resp struct {
		Domains    []domainResponse `json:"domains"`
		NextCursor string           `json:"next_cursor"`
	}
	raw, err := c.do(http.MethodGet, "/api/v1/domains"+query, nil, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderDomains(out, resp.Domains)
	if resp.NextCursor != "" {
		_, _ = fmt.Fprintf(out, "next_cursor: %s (pass as --after)\n", resp.NextCursor)
	}
	return nil
}

func renderDomains(out io.Writer, domains []domainResponse) {
	rows := make([][]string, 0, len(domains))
	for _, d := range domains {
		rows = append(rows, []string{d.Domain, d.Status, d.DNSMode, strconv.FormatBool(d.ReceivingEnabled), d.CreatedAt})
	}
	renderTable(out, []string{"DOMAIN", "STATUS", "DNS_MODE", "RECEIVING", "CREATED"}, rows)
}

func runDomainsCreate(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("domains create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("dns-mode", "records", "DNS mode: records or delegated")
	if err := parseDomainFlags(fs, args, "DOMAIN"); err != nil {
		return err
	}
	if *mode != "records" && *mode != "delegated" {
		return &usageError{"--dns-mode must be records or delegated"}
	}
	req := struct {
		Domain  string `json:"domain"`
		DNSMode string `json:"dns_mode"`
	}{Domain: args[0], DNSMode: *mode}
	return requestDomain(c, http.MethodPost, "/api/v1/domains", req, out)
}

func runDomainsUpdate(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("domains update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("dns-mode", "", "DNS mode: records or delegated")
	receiving := fs.Bool("receiving-enabled", false, "enable or disable receiving (--receiving-enabled=true|false)")
	clearWebhooks := fs.Bool("clear-webhooks", false, "clear all webhook URLs")
	clearMailboxes := fs.Bool("clear-mailboxes", false, "clear all mailboxes")
	var webhooks, mailboxes repeatedFlag
	fs.Var(&webhooks, "webhook", "webhook URL (repeatable; replaces the existing list)")
	fs.Var(&mailboxes, "mailbox", "mailbox address (repeatable; replaces the existing list)")
	if err := parseDomainFlags(fs, args, "DOMAIN"); err != nil {
		return err
	}
	seen := visitedFlags(fs)
	req := make(map[string]any)
	if seen["dns-mode"] {
		if *mode != "records" && *mode != "delegated" {
			return &usageError{"--dns-mode must be records or delegated"}
		}
		req["dns_mode"] = *mode
	}
	if seen["receiving-enabled"] {
		req["receiving_enabled"] = *receiving
	}
	if *clearWebhooks && seen["webhook"] {
		return &usageError{"--webhook and --clear-webhooks are mutually exclusive"}
	}
	if *clearMailboxes && seen["mailbox"] {
		return &usageError{"--mailbox and --clear-mailboxes are mutually exclusive"}
	}
	if seen["webhook"] {
		req["webhook_urls"] = webhooks
	}
	if seen["mailbox"] {
		req["mailboxes"] = mailboxes
	}
	if *clearWebhooks {
		req["webhook_urls"] = []string{}
	}
	if *clearMailboxes {
		req["mailboxes"] = []string{}
	}
	if len(req) == 0 {
		return &usageError{"domains update requires at least one change"}
	}
	return requestDomain(c, http.MethodPatch, domainPath(args[0]), req, out)
}

func requestDomain(c *client, method, path string, body any, out io.Writer) error {
	var resp domainResponse
	raw, err := c.do(method, path, body, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderDomain(out, &resp)
	return nil
}

func runDomainAction(c *client, action string, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("domains "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := parseDomainFlags(fs, args, "DOMAIN"); err != nil {
		return err
	}
	path := domainPath(args[0])
	switch action {
	case "get":
		return requestDomain(c, http.MethodGet, path, nil, out)
	case "verify":
		return requestDomain(c, http.MethodPost, path+"/verify", nil, out)
	case "delete":
		if _, err := c.do(http.MethodDelete, path, nil, nil); err != nil {
			return err
		}
		if !c.jsonOut {
			_, _ = fmt.Fprintf(out, "deleted domain %s\n", args[0])
		}
	case "records":
		return runDomainRecords(c, args[0], out, net.DefaultResolver)
	}
	return nil
}

func renderDomain(out io.Writer, d *domainResponse) {
	renderKV(out, [][2]string{
		{"domain", d.Domain}, {"status", d.Status}, {"dns_mode", d.DNSMode},
		{"receiving_enabled", strconv.FormatBool(d.ReceivingEnabled)},
		{"webhook_urls", orDash(strings.Join(d.WebhookURLs, ", "))},
		{"mailboxes", orDash(strings.Join(d.Mailboxes, ", "))},
		{"nameservers", orDash(strings.Join(d.Nameservers, ", "))},
		{"verified_at", orDash(d.VerifiedAt)}, {"last_checked_at", orDash(d.LastCheckedAt)},
		{"created_at", d.CreatedAt}, {"updated_at", d.UpdatedAt},
	})
	if d.OwnershipRecord != nil {
		renderDomainRecords(out, []domainDNSRecord{*d.OwnershipRecord})
	}
}

func renderDomainRecords(out io.Writer, records []domainDNSRecord) {
	rows := make([][]string, 0, len(records))
	for _, r := range records {
		rows = append(rows, []string{r.Name, r.Type, r.Value})
	}
	renderTable(out, []string{"NAME", "TYPE", "VALUE"}, rows)
}
