package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
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

// runWhoami implements `wagon whoami` — GET /who.
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
	if c.jsonOut {
		return printPretty(out, raw)
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
	return nil
}
