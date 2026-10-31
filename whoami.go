package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// whoResponse mirrors httpd.WhoResponse (GET /who), trimmed to the fields
// wagon renders; json.Unmarshal ignores unrecognized fields.
type whoResponse struct {
	ClientID         string   `json:"client_id"`
	Senders          []string `json:"senders"`
	Webhooks         []string `json:"webhooks"`
	NotBefore        string   `json:"not_before"`
	NotAfter         string   `json:"not_after"`
	Serial           string   `json:"serial"`
	Revoked          bool     `json:"revoked"`
	KeyType          string   `json:"key_type"`
	Valid            bool     `json:"valid"`
	Reasons          []string `json:"reasons"`
	ExpiresInDays    int      `json:"expires_in_days"`
	RenewRecommended bool     `json:"renew_recommended"`
}

// runWhoami combines the certificate identity with all owned domains.
func runWhoami(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		return &usageError{"whoami takes no arguments"}
	}

	var resp whoResponse
	raw, err := c.do(http.MethodGet, "/who", nil, &resp)
	if err != nil {
		return err
	}
	domainJSON, err := whoamiDomains(c)
	if err != nil {
		return err
	}
	if c.jsonOut {
		var report map[string]json.RawMessage
		if err := json.Unmarshal(raw, &report); err != nil {
			return fmt.Errorf("decode identity: %w", err)
		}
		if report == nil {
			return fmt.Errorf("decode identity: expected an object")
		}
		report["domains"] = domainJSON
		combined, err := json.Marshal(report)
		if err != nil {
			return fmt.Errorf("encode identity report: %w", err)
		}
		return printPretty(out, combined)
	}
	var domains []domainResponse
	if err := json.Unmarshal(domainJSON, &domains); err != nil {
		return fmt.Errorf("decode domains: %w", err)
	}
	renderKV(out, [][2]string{
		{"client_id", resp.ClientID},
		{"valid", fmt.Sprintf("%t", resp.Valid)},
		{"revoked", fmt.Sprintf("%t", resp.Revoked)},
		{"key_type", resp.KeyType},
		{"serial", resp.Serial},
		{"not_before", resp.NotBefore},
		{"not_after", resp.NotAfter},
		{"expires_in_days", fmt.Sprintf("%d", resp.ExpiresInDays)},
		{"renew_recommended", fmt.Sprintf("%t", resp.RenewRecommended)},
		{"reasons", orDash(strings.Join(resp.Reasons, ", "))},
		{"senders", strings.Join(resp.Senders, ", ")},
		{"webhooks", strings.Join(resp.Webhooks, ", ")},
	})
	if len(domains) == 0 {
		_, _ = fmt.Fprintln(out, "\ndomains: none")
		return nil
	}
	_, _ = fmt.Fprintln(out, "\ndomains:")
	renderDomains(out, domains)
	return nil
}

// whoamiDomains collects every page and preserves each domain's wire fields.
func whoamiDomains(c *client) ([]byte, error) {
	domains := make([]json.RawMessage, 0)
	seen := make(map[string]bool)
	path := "/api/v1/domains"
	for {
		var page struct {
			Domains    []json.RawMessage `json:"domains"`
			NextCursor string            `json:"next_cursor"`
		}
		if _, err := c.do(http.MethodGet, path, nil, &page); err != nil {
			return nil, fmt.Errorf("list domains: %w", err)
		}
		domains = append(domains, page.Domains...)
		if page.NextCursor == "" {
			break
		}
		if seen[page.NextCursor] {
			return nil, fmt.Errorf("list domains: server repeated a pagination cursor")
		}
		seen[page.NextCursor] = true
		path = "/api/v1/domains?" + url.Values{"after": {page.NextCursor}}.Encode()
	}
	return json.Marshal(domains)
}
