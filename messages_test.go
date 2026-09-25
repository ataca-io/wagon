package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// captureQueryHandler records the raw query of each request and answers with a
// fixed one-message page.
func captureQueryHandler(got *string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*got = r.URL.RawQuery
		jsonHandler(http.StatusOK, map[string]any{
			"messages": []map[string]any{{
				"id": "01jx", "status": "delivered", "direction": "outbound",
				"from": "a@x.com", "to": []string{"b@y.com"}, "size": 42,
				"created_at": "2026-09-19T10:00:00Z",
			}},
		})(w, r)
	}
}

// Each flag must map to the query parameter the API expects: --from is
// ?sender and --to is ?recipient, matching the dashboard's names.
func TestMessagesListQuery(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "no flags", args: nil, want: nil},
		{name: "status", args: []string{"--status", "bounced"}, want: []string{"status=bounced"}},
		{name: "from maps to sender", args: []string{"--from", "a@x.com"}, want: []string{"sender=a%40x.com"}},
		{name: "to maps to recipient", args: []string{"--to", "b@y.com"}, want: []string{"recipient=b%40y.com"}},
		{name: "limit", args: []string{"--limit", "5"}, want: []string{"limit=5"}},
		{name: "cursor", args: []string{"--before", "01jz"}, want: []string{"before=01jz"}},
		{
			name: "window",
			args: []string{"--since", "2026-09-01", "--until", "2026-09-19"},
			want: []string{"since=2026-09-01", "until=2026-09-19"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pki := mintClientCert(t)
			var query string
			url, caFile := newTLSServer(t, captureQueryHandler(&query), pki.clientCAs)

			var stdout, stderr bytes.Buffer
			args := baseArgs(url, pki, caFile, append([]string{"messages", "list"}, tt.args...)...)
			if got := run(args, &stdout, &stderr); got != 0 {
				t.Fatalf("run exit = %d, want 0; stderr = %s", got, stderr.String())
			}
			for _, want := range tt.want {
				if !strings.Contains(query, want) {
					t.Errorf("query = %q, want it to contain %q", query, want)
				}
			}
			if tt.want == nil && query != "" {
				t.Errorf("query = %q, want empty", query)
			}
			if !strings.Contains(stdout.String(), "01jx") {
				t.Errorf("stdout = %q, want the message id", stdout.String())
			}
		})
	}
}

// A zero limit is never sent: the server rejects it, and the flag's zero value
// means "unset" rather than "ask for nothing".
func TestMessagesListOmitsZeroLimit(t *testing.T) {
	pki := mintClientCert(t)
	var query string
	url, caFile := newTLSServer(t, captureQueryHandler(&query), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	args := baseArgs(url, pki, caFile, "messages", "list", "--limit", "0")
	if got := run(args, &stdout, &stderr); got != 0 {
		t.Fatalf("run exit = %d, want 0; stderr = %s", got, stderr.String())
	}
	if strings.Contains(query, "limit") {
		t.Errorf("query = %q, want no limit parameter", query)
	}
}

// next_before becomes a copy-pasteable follow-up command.
func TestMessagesListPrintsCursor(t *testing.T) {
	pki := mintClientCert(t)
	url, caFile := newTLSServer(t, jsonHandler(http.StatusOK, map[string]any{
		"messages": []map[string]any{{
			"id": "01jx", "status": "queued", "direction": "outbound",
			"from": "a@x.com", "to": []string{"b@y.com"}, "size": 1,
			"created_at": "2026-09-19T10:00:00Z",
		}},
		"next_before": "01jw",
	}), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	args := baseArgs(url, pki, caFile, "messages", "list")
	if got := run(args, &stdout, &stderr); got != 0 {
		t.Fatalf("run exit = %d, want 0; stderr = %s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "--before 01jw") {
		t.Errorf("stdout = %q, want the next-page hint", stdout.String())
	}
}

func TestMessagesListUsageErrors(t *testing.T) {
	pki := mintClientCert(t)
	url, caFile := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonHandler(http.StatusOK, map[string]any{"messages": []any{}})(w, r)
	}), pki.clientCAs)

	tests := []struct {
		name string
		verb []string
	}{
		{name: "positional argument", verb: []string{"messages", "list", "01jx"}},
		{name: "unknown flag", verb: []string{"messages", "list", "--bogus"}},
		{name: "unknown subcommand", verb: []string{"messages", "bogus"}},
		{name: "no subcommand", verb: []string{"messages"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(baseArgs(url, pki, caFile, tt.verb...), &stdout, &stderr); got != 1 {
				t.Errorf("run(%v) exit = %d, want 1", tt.verb, got)
			}
		})
	}
}
