package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWhoamiDomains(t *testing.T) {
	pki := mintClientCert(t)
	const identity = `{"client_id":"testclient","senders":["sender@unregistered.example"],"future_field":9007199254740993}`
	const firstDomain = `{"domain":"first.example","status":"verified","dns_mode":"records","receiving_enabled":true,"verified_at":null,"future_field":9007199254740993}`
	const secondDomain = `{"domain":"second.example","status":"pending","dns_mode":"delegated","receiving_enabled":false}`
	tests := []struct {
		name    string
		pages   []string
		want    string
		domains []string
	}{
		{"empty", []string{`{"domains":[]}`}, `[]`, nil},
		{"one page", []string{`{"domains":[` + firstDomain + `]}`}, `[` + firstDomain + `]`, []string{"first.example"}},
		{
			"multiple pages",
			[]string{`{"domains":[` + firstDomain + `],"next_cursor":"opaque+/=?&"}`, `{"domains":[` + secondDomain + `]}`},
			`[` + firstDomain + `,` + secondDomain + `]`, []string{"first.example", "second.example"},
		},
	}
	for _, tt := range tests {
		for _, jsonOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tt.name, jsonOut), func(t *testing.T) {
				page := 0
				mux := http.NewServeMux()
				mux.HandleFunc("GET /who", func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, identity)
				})
				mux.HandleFunc("GET /api/v1/domains", func(w http.ResponseWriter, r *http.Request) {
					if page >= len(tt.pages) {
						t.Error("unexpected extra domain request")
						http.Error(w, "unexpected request", http.StatusInternalServerError)
						return
					}
					wantQuery := ""
					if page > 0 {
						wantQuery = "after=opaque%2B%2F%3D%3F%26"
					}
					if r.URL.RawQuery != wantQuery {
						t.Errorf("query = %q, want %q", r.URL.RawQuery, wantQuery)
					}
					_, _ = io.WriteString(w, tt.pages[page])
					page++
				})
				server, ca := newTLSServer(t, mux, pki.clientCAs)
				args := baseArgs(server, pki, ca)
				if jsonOut {
					args = append(args, "--json")
				}
				var stdout, stderr bytes.Buffer
				if code := run(append(args, "whoami"), &stdout, &stderr); code != 0 {
					t.Fatalf("exit = %d; stderr = %s", code, stderr.String())
				}
				if page != len(tt.pages) {
					t.Errorf("fetched %d pages, want %d", page, len(tt.pages))
				}
				if jsonOut {
					var report map[string]json.RawMessage
					if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
						t.Fatal(err)
					}
					if string(report["client_id"]) != `"testclient"` || string(report["future_field"]) != "9007199254740993" {
						t.Errorf("identity fields changed: %s", stdout.String())
					}
					var compact bytes.Buffer
					if err := json.Compact(&compact, report["domains"]); err != nil {
						t.Fatal(err)
					}
					if compact.String() != tt.want {
						t.Errorf("domains = %s, want %s", compact.String(), tt.want)
					}
					return
				}
				if !strings.Contains(stdout.String(), "testclient") || !strings.Contains(stdout.String(), "domains:") {
					t.Errorf("missing identity or domains: %s", stdout.String())
				}
				if len(tt.domains) == 0 && !strings.Contains(stdout.String(), "domains: none") {
					t.Errorf("empty domains not reported: %s", stdout.String())
				}
				for _, domain := range tt.domains {
					if !strings.Contains(stdout.String(), domain) {
						t.Errorf("missing domain %q: %s", domain, stdout.String())
					}
				}
				if len(tt.domains) > 0 && (!strings.Contains(stdout.String(), "verified") || !strings.Contains(stdout.String(), "RECEIVING")) {
					t.Errorf("missing domain details: %s", stdout.String())
				}
			})
		}
	}
}

func TestWhoamiDomainErrors(t *testing.T) {
	pki := mintClientCert(t)
	tests := []struct {
		name     string
		status   int
		response string
		wantCode int
		wantErr  string
	}{
		{"forbidden", 403, `{"error":"forbidden","message":"client cannot manage domains"}`, 2, "list domains: forbidden"},
		{"missing endpoint", 404, `{"error":"not_found"}`, 3, "list domains: not_found"},
		{"unavailable", 503, `{"error":"unavailable"}`, 4, "list domains: unavailable"},
		{"malformed JSON", 200, `{`, 4, "list domains: decode response"},
		{"repeated cursor", 200, `{"domains":[],"next_cursor":"same"}`, 4, "server repeated a pagination cursor"},
	}
	for _, tt := range tests {
		for _, jsonOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tt.name, jsonOut), func(t *testing.T) {
				calls := 0
				mux := http.NewServeMux()
				mux.Handle("GET /who", jsonHandler(200, map[string]string{"client_id": "testclient"}))
				mux.HandleFunc("GET /api/v1/domains", func(w http.ResponseWriter, r *http.Request) {
					calls++
					if calls > 2 {
						t.Error("pagination did not stop")
						http.Error(w, "too many pages", http.StatusInternalServerError)
						return
					}
					if calls == 1 {
						_, _ = io.WriteString(w, `{"domains":[{"domain":"first.example"}],"next_cursor":"same"}`)
						return
					}
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.response)
				})
				server, ca := newTLSServer(t, mux, pki.clientCAs)
				args := baseArgs(server, pki, ca)
				if jsonOut {
					args = append(args, "--json")
				}
				var stdout, stderr bytes.Buffer
				if code := run(append(args, "whoami"), &stdout, &stderr); code != tt.wantCode {
					t.Errorf("exit = %d, want %d; stderr = %s", code, tt.wantCode, stderr.String())
				}
				if stdout.Len() != 0 || !strings.Contains(stderr.String(), tt.wantErr) {
					t.Errorf("stdout = %s; stderr = %s; want error containing %q", stdout.String(), stderr.String(), tt.wantErr)
				}
			})
		}
	}
}
