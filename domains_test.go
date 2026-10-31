package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const domainFixture = `{
	"domain":"example.com","status":"pending","dns_mode":"records","receiving_enabled":false,
	"webhook_urls":["https://example.com/mail"],"mailboxes":["help@example.com"],
	"ownership_record":{"name":"_rail.example.com","type":"TXT","value":"test-proof"},
	"verified_at":null,"last_checked_at":null,
	"created_at":"2026-10-31T10:00:00Z","updated_at":"2026-10-31T11:00:00Z","future_field":42
}`

const forwardFixture = `{
	"local_part":"help","destination":"yavosh@gmail.com","enabled":false,"from_munging":"arc",
	"consecutive_failures":2,"disabled_at":null,"disabled_reason":"hard_bounce","created_at":"2026-10-31T10:00:00Z"
}`

// Exercise all eleven operations through the CLI and an mTLS server.
// Check exact requests and preserve complete responses in JSON mode.
func TestDomainsAPI(t *testing.T) {
	pki := mintClientCert(t)
	tests := []struct {
		name     string
		args     []string
		method   string
		uri      string
		body     string
		status   int
		response string
		output   []string
	}{
		{
			name: "list", args: []string{"list"}, method: http.MethodGet, uri: "/api/v1/domains", status: 200,
			response: `{"domains":[` + domainFixture + `],"next_cursor":"next-domain"}`,
			output:   []string{"example.com", "pending", "records", "false", "next_cursor: next-domain (pass as --after)"},
		},
		{
			name: "list page", args: []string{"list", "--limit", "2", "--after", "opaque+/=?&"},
			method: http.MethodGet, uri: "/api/v1/domains?after=opaque%2B%2F%3D%3F%26&limit=2", status: 200,
			response: `{"domains":[]}`, output: []string{"DOMAIN"},
		},
		{
			name: "get", args: []string{"get", "example.com"}, method: http.MethodGet,
			uri: "/api/v1/domains/example.com", status: 200, response: domainFixture,
			output: []string{"example.com", "https://example.com/mail", "help@example.com", "_rail.example.com", "TXT", "test-proof", "verified_at", "last_checked_at", "2026-10-31T11:00:00Z"},
		},
		{
			name: "create defaults", args: []string{"create", "example.com"}, method: http.MethodPost,
			uri: "/api/v1/domains", body: `{"domain":"example.com","dns_mode":"records"}`, status: 201,
			response: domainFixture, output: []string{"test-proof"},
		},
		{
			name: "create delegated", args: []string{"create", "example.com", "--dns-mode", "delegated"},
			method: http.MethodPost, uri: "/api/v1/domains", body: `{"domain":"example.com","dns_mode":"delegated"}`, status: 201,
			response: `{"domain":"example.com","dns_mode":"delegated","nameservers":["ns1.example.net","ns2.example.net"],"verified_at":null}`,
			output:   []string{"delegated", "ns1.example.net, ns2.example.net"},
		},
		{
			name: "update routing", args: []string{"update", "example.com", "--dns-mode", "records", "--receiving-enabled=true", "--webhook", "https://example.com/one", "--webhook", "https://example.com/two", "--mailbox", "one@example.com", "--mailbox", "two@example.com"},
			method: http.MethodPatch, uri: "/api/v1/domains/example.com",
			body:   `{"dns_mode":"records","receiving_enabled":true,"webhook_urls":["https://example.com/one","https://example.com/two"],"mailboxes":["one@example.com","two@example.com"]}`,
			status: 200, response: domainFixture, output: []string{"example.com"},
		},
		{
			name: "update false preserves omitted fields", args: []string{"update", "example.com", "--receiving-enabled=false"},
			method: http.MethodPatch, uri: "/api/v1/domains/example.com", body: `{"receiving_enabled":false}`,
			status: 200, response: domainFixture, output: []string{"false"},
		},
		{
			name: "clear lists", args: []string{"update", "example.com", "--clear-webhooks", "--clear-mailboxes"},
			method: http.MethodPatch, uri: "/api/v1/domains/example.com", body: `{"webhook_urls":[],"mailboxes":[]}`,
			status: 200, response: domainFixture, output: []string{"example.com"},
		},
		{
			name: "delete", args: []string{"delete", "example.com"}, method: http.MethodDelete,
			uri: "/api/v1/domains/example.com", status: 204, output: []string{"deleted domain example.com"},
		},
		{
			name: "records", args: []string{"records", "example.com"}, method: http.MethodGet,
			uri: "/api/v1/domains/example.com/records", status: 200,
			response: `{"records":[{"name":"example.com","type":"MX","value":"10 mx.example.com"},{"name":"_rail.example.com","type":"TXT","value":"test-proof"}]}`,
			output:   []string{"MX", "10 mx.example.com", "TXT", "test-proof"},
		},
		{
			name: "verify", args: []string{"verify", "example.com"}, method: http.MethodPost,
			uri: "/api/v1/domains/example.com/verify", status: 200, response: domainFixture, output: []string{"example.com"},
		},
		{
			name: "forwards list", args: []string{"forwards", "list", "example.com"}, method: http.MethodGet,
			uri: "/api/v1/domains/example.com/forwards", status: 200,
			response: `{"forwards":[` + forwardFixture + `],"next_cursor":"next-forward"}`,
			output:   []string{"help", "yavosh@gmail.com", "arc", "hard_bounce", "next_cursor: next-forward (pass as --after)"},
		},
		{
			name: "forwards page", args: []string{"forwards", "list", "example.com", "--limit", "100", "--after", "next+/="},
			method: http.MethodGet, uri: "/api/v1/domains/example.com/forwards?after=next%2B%2F%3D&limit=100",
			status: 200, response: `{"forwards":[]}`, output: []string{"LOCAL_PART"},
		},
		{
			name: "forward create", args: []string{"forwards", "create", "example.com", "help", "yavosh@gmail.com", "--from-munging", "arc"},
			method: http.MethodPost, uri: "/api/v1/domains/example.com/forwards",
			body:   `{"local_part":"help","destination":"yavosh@gmail.com","from_munging":"arc"}`,
			status: 201, response: forwardFixture, output: []string{"yavosh@gmail.com", "hard_bounce", "consecutive_failures"},
		},
		{
			name: "forward create defaults", args: []string{"forwards", "create", "example.com", "help", "yavosh@gmail.com"},
			method: http.MethodPost, uri: "/api/v1/domains/example.com/forwards",
			body:   `{"local_part":"help","destination":"yavosh@gmail.com","from_munging":""}`,
			status: 201, response: forwardFixture, output: []string{"yavosh@gmail.com"},
		},
		{
			name: "forward hyphenated local part", args: []string{"forwards", "create", "example.com", "-help", "yavosh@gmail.com"},
			method: http.MethodPost, uri: "/api/v1/domains/example.com/forwards",
			body:   `{"local_part":"-help","destination":"yavosh@gmail.com","from_munging":""}`,
			status: 201, response: forwardFixture, output: []string{"yavosh@gmail.com"},
		},
		{
			name: "forward update false", args: []string{"forwards", "update", "example.com", "help", "yavosh@gmail.com", "--enabled=false"},
			method: http.MethodPatch, uri: "/api/v1/domains/example.com/forwards/help/yavosh@gmail.com", body: `{"enabled":false}`,
			status: 200, response: `{"forward":` + forwardFixture + `,"suppression_cleared":false,"still_suppressed_reason":"complaint"}`,
			output: []string{"suppression_cleared", "still_suppressed_reason", "complaint"},
		},
		{
			name: "forward reset munging", args: []string{"forwards", "update", "example.com", "help", "yavosh@gmail.com", "--from-munging="},
			method: http.MethodPatch, uri: "/api/v1/domains/example.com/forwards/help/yavosh@gmail.com", body: `{"from_munging":""}`,
			status: 200, response: `{"forward":` + forwardFixture + `,"suppression_cleared":false}`,
			output: []string{"suppression_cleared"},
		},
		{
			name: "forward enable and set munging", args: []string{"forwards", "update", "example.com", "help", "yavosh@gmail.com", "--enabled=true", "--from-munging", "auto"},
			method: http.MethodPatch, uri: "/api/v1/domains/example.com/forwards/help/yavosh@gmail.com", body: `{"enabled":true,"from_munging":"auto"}`,
			status: 200, response: `{"forward":` + forwardFixture + `,"suppression_cleared":true}`,
			output: []string{"suppression_cleared", "true"},
		},
		{
			name: "forward delete escapes path", args: []string{"forwards", "delete", "example.com", "help/tag?x#y", "help/tag?x#y@nar.cy"},
			method: http.MethodDelete, uri: "/api/v1/domains/example.com/forwards/help%2Ftag%3Fx%23y/help%2Ftag%3Fx%23y@nar.cy",
			status: 204, output: []string{"deleted forward help/tag?x#y@example.com -> help/tag?x#y@nar.cy"},
		},
	}
	for _, tt := range tests {
		for _, jsonOut := range []bool{false, true} {
			name := tt.name
			if jsonOut {
				name += " JSON"
			}
			t.Run(name, func(t *testing.T) {
				calls := 0
				server, ca := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.Method != tt.method || r.RequestURI != tt.uri {
						t.Errorf("request = %s %s, want %s %s", r.Method, r.RequestURI, tt.method, tt.uri)
					}
					if r.Header.Get("Accept") != "application/json" {
						t.Error("request must accept JSON")
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if tt.body == "" {
						if len(body) != 0 {
							t.Errorf("unexpected body: %s", body)
						}
					} else {
						if r.Header.Get("Content-Type") != "application/json" {
							t.Error("request body must be JSON")
						}
						assertDomainJSON(t, body, tt.body)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.response)
				}), pki.clientCAs)
				args := baseArgs(server, pki, ca)
				if jsonOut {
					args = append(args, "--json")
				}
				args = append(args, "domains")
				args = append(args, tt.args...)
				var stdout, stderr bytes.Buffer
				if code := run(args, &stdout, &stderr); code != 0 {
					t.Fatalf("exit = %d; stderr = %s", code, stderr.String())
				}
				if calls != 1 {
					t.Fatalf("requests = %d, want 1", calls)
				}
				if jsonOut {
					if tt.status == http.StatusNoContent {
						if stdout.Len() != 0 {
							t.Errorf("204 response produced output: %s", stdout.String())
						}
					} else {
						assertDomainJSON(t, stdout.Bytes(), tt.response)
					}
					return
				}
				for _, want := range tt.output {
					if !strings.Contains(stdout.String(), want) {
						t.Errorf("output missing %q: %s", want, stdout.String())
					}
				}
			})
		}
	}
}

func assertDomainJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var actual, expected any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Errorf("invalid JSON: %v", err)
		return
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("JSON = %s, want %s", got, want)
	}
}

func TestDomainsUsageErrors(t *testing.T) {
	tests := [][]string{
		nil, {"bogus"}, {"list", "extra"}, {"list", "--bogus"}, {"list", "--limit", "0"},
		{"list", "--limit", "101"}, {"list", "--limit", "-1"}, {"list", "--limit", "bad"},
		{"get"}, {"get", ""}, {"get", "example.com", "extra"}, {"delete"}, {"records"}, {"verify"},
		{"create"}, {"create", "--dns-mode", "records"}, {"create", "example.com", "--dns-mode", "bad"},
		{"update", "example.com"}, {"update", "example.com", "--dns-mode", ""},
		{"update", "example.com", "--receiving-enabled", "false"},
		{"update", "example.com", "--webhook", "https://example.com/mail", "--clear-webhooks"},
		{"update", "example.com", "--mailbox", "help@example.com", "--clear-mailboxes"},
		{"update", "example.com", "--clear-mailboxes=false"},
		{"forwards"}, {"forwards", "bogus"}, {"forwards", "list"},
		{"forwards", "list", "example.com", "--limit", "101"},
		{"forwards", "create", "example.com", "help"},
		{"forwards", "create", "example.com", "help", "yavosh@gmail.com", "--from-munging", "bad"},
		{"forwards", "update", "example.com", "help", "yavosh@gmail.com"},
		{"forwards", "update", "example.com", "help", "yavosh@gmail.com", "--enabled", "false"},
		{"forwards", "delete", "example.com", "help", "yavosh@gmail.com", "extra"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			// A nil client proves invalid input fails before any network request.
			err := runDomains(nil, args, io.Discard, io.Discard)
			if code := exitCode(err); code != 1 {
				t.Errorf("exit = %d, want 1; error = %v", code, err)
			}
		})
	}
}

func TestDomainsAPIErrors(t *testing.T) {
	pki := mintClientCert(t)
	tests := []struct {
		name   string
		args   []string
		status int
		code   int
	}{
		{"authentication", []string{"list"}, 401, 2},
		{"forbidden", []string{"create", "example.com"}, 403, 2},
		{"not found", []string{"get", "example.com"}, 404, 3},
		{"ownership conflict", []string{"verify", "example.com"}, 409, 3},
		{"certificate reference", []string{"delete", "example.com"}, 409, 3},
		{"rate limited", []string{"create", "example.com"}, 429, 3},
		{"unavailable", []string{"records", "example.com"}, 503, 4},
		{"invalid routing", []string{"update", "example.com", "--receiving-enabled=true"}, 400, 3},
		{"forward list", []string{"forwards", "list", "example.com"}, 404, 3},
		{"forward create", []string{"forwards", "create", "example.com", "help", "yavosh@gmail.com"}, 409, 3},
		{"forward update", []string{"forwards", "update", "example.com", "help", "yavosh@gmail.com", "--enabled=true"}, 409, 3},
		{"forward delete", []string{"forwards", "delete", "example.com", "help", "yavosh@gmail.com"}, 404, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, ca := newTLSServer(t, jsonHandler(tt.status, map[string]string{"error": "domain_error", "message": tt.name}), pki.clientCAs)
			args := baseArgs(server, pki, ca, "domains")
			var stdout, stderr bytes.Buffer
			if code := run(append(args, tt.args...), &stdout, &stderr); code != tt.code {
				t.Errorf("exit = %d, want %d", code, tt.code)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "domain_error: "+tt.name) {
				t.Errorf("stdout = %s; stderr = %s", stdout.String(), stderr.String())
			}
		})
	}
}
