package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

const domainRecordsFixture = `{"domain":"example.com","dns_mode":"records","receiving_enabled":false,"records":[{"name":"_rail-challenge.example.com","type":"TXT","value":"proof","purpose":"ownership","requirement":"required"},{"name":"example.com","type":"TXT","value":"v=spf1 include:smtp.example.com ~all","purpose":"spf","requirement":"recommended"}],"future_field":9007199254740993}`
const domainCheckFixture = `{"domain":"example.com","dns_mode":"records","receiving_enabled":false,"checked_at":"2026-10-31T12:00:00Z","results":[{"check":"dns.spf","subject":"example.com","status":"pass","observed":"v=spf1 include:external.example include:smtp.example.com ~all","detail":"all sender IPs pass"},{"check":"dns.mx","subject":"example.com","status":"skip","detail":"receiving disabled"}],"future_check_field":9007199254740993}`

type domainCheckTransport func(*http.Request) (*http.Response, error)

func (f domainCheckTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRunDomainRecords(t *testing.T) {
	tests := []struct {
		name     string
		records  string
		check    string
		status   int
		wantCode int
		wantErr  string
	}{
		{name: "sending only", check: domainCheckFixture},
		{name: "warning", check: strings.Replace(domainCheckFixture, `"status":"pass"`, `"status":"warn"`, 1)},
		{name: "failure", check: strings.Replace(domainCheckFixture, `"status":"pass"`, `"status":"fail"`, 1), wantCode: 5},
		{name: "lookup error", check: strings.Replace(domainCheckFixture, `"status":"pass"`, `"status":"error"`, 1), wantCode: 4},
		{name: "error overrides failure", check: strings.Replace(strings.Replace(domainCheckFixture, `"status":"pass"`, `"status":"fail"`, 1), `"status":"skip"`, `"status":"error"`, 1), wantCode: 4},
		{name: "unknown status", check: strings.Replace(domainCheckFixture, `"status":"pass"`, `"status":"unknown"`, 1), wantCode: 4},
		{name: "missing check name", check: strings.Replace(domainCheckFixture, `"check":"dns.spf",`, ``, 1), wantCode: 4},
		{name: "missing subject", check: strings.Replace(domainCheckFixture, `"subject":"example.com",`, ``, 1), wantCode: 4},
		{name: "empty results", check: `{"domain":"example.com","dns_mode":"records","receiving_enabled":false,"checked_at":"2026-10-31T12:00:00Z","results":[]}`, wantCode: 4, wantErr: "no results"},
		{name: "missing metadata", check: `{"results":[{"check":"dns.spf","subject":"example.com","status":"pass"}]}`, wantCode: 4},
		{name: "missing receiving", check: strings.Replace(domainCheckFixture, `"receiving_enabled":false,`, ``, 1), wantCode: 4},
		{name: "missing timestamp", check: strings.Replace(domainCheckFixture, `"checked_at":"2026-10-31T12:00:00Z",`, ``, 1), wantCode: 4},
		{name: "changed receiving", check: strings.Replace(domainCheckFixture, `"receiving_enabled":false`, `"receiving_enabled":true`, 1), wantCode: 4, wantErr: "settings changed"},
		{name: "changed DNS mode", check: strings.Replace(domainCheckFixture, `"dns_mode":"records"`, `"dns_mode":"delegated"`, 1), wantCode: 4},
		{name: "changed domain", check: strings.Replace(domainCheckFixture, `"domain":"example.com"`, `"domain":"other.example"`, 1), wantCode: 4},
		{name: "old records shape", records: `{"records":[]}`, check: domainCheckFixture, wantCode: 4, wantErr: "upgrade rail"},
		{name: "null", check: `null`, wantCode: 4},
		{name: "empty body", wantCode: 4},
		{name: "malformed body", check: `{`, wantCode: 4},
		{name: "unavailable", check: `{"error":"unavailable","message":"retry later"}`, status: 503, wantCode: 4},
		{name: "old server", check: `404 page not found`, status: 404, wantCode: 3, wantErr: "upgrade rail"},
		{name: "unsupported method", status: 405, wantCode: 3, wantErr: "upgrade rail"},
		{name: "not implemented", status: 501, wantCode: 4, wantErr: "upgrade rail"},
		{name: "auth rejected", status: 403, wantCode: 2},
		{name: "rate limited", status: 429, wantCode: 3},
	}
	for _, tt := range tests {
		for _, jsonOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tt.name, jsonOut), func(t *testing.T) {
				var stdout bytes.Buffer
				var calls []string
				c := &client{base: "https://rail.example", jsonOut: jsonOut}
				c.http = &http.Client{Transport: domainCheckTransport(func(r *http.Request) (*http.Response, error) {
					calls = append(calls, r.Method+" "+r.URL.Path)
					body, status := tt.check, tt.status
					if status == 0 {
						status = http.StatusOK
					}
					if r.Method == http.MethodGet {
						body, status = tt.records, http.StatusOK
						if body == "" {
							body = domainRecordsFixture
						}
					} else {
						if r.Body != nil {
							t.Error("check request must have no body")
						}
						if !jsonOut && !strings.Contains(stdout.String(), "DNS guidance:") {
							t.Errorf("guidance missing before check request: %s", stdout.String())
						}
						if jsonOut && stdout.Len() != 0 {
							t.Error("JSON report must be one complete document")
						}
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})}
				err := runDomainRecords(c, "example.com", &stdout)
				if exitCode(err) != tt.wantCode {
					t.Errorf("exit = %d, want %d; error = %v", exitCode(err), tt.wantCode, err)
				}
				if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
					t.Errorf("error = %v, want %q", err, tt.wantErr)
				}
				if want := []string{"GET /api/v1/domains/example.com/records", "POST /api/v1/domains/example.com/check"}; !slices.Equal(calls, want) {
					t.Errorf("requests = %v, want %v", calls, want)
				}
				if jsonOut {
					var report map[string]json.RawMessage
					if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
						t.Fatal(err)
					}
					if tt.records == "" && string(report["future_field"]) != "9007199254740993" {
						t.Error("records fields were not preserved")
					}
					if tt.wantCode != 0 && len(report["validation_error"]) == 0 {
						t.Errorf("missing error in failed report: %s", stdout.String())
					}
					if tt.name == "sending only" {
						assertDomainJSON(t, report["validation"], tt.check)
					}
					return
				}
				if tt.records == "" {
					for _, want := range []string{"Sending only", "DNS guidance:", "proof", "ownership", "required", "recommended", "Merge SPF", "existing DMARC", "DNS validation (rail resolver):"} {
						if !strings.Contains(stdout.String(), want) {
							t.Errorf("output missing %q: %s", want, stdout.String())
						}
					}
				}
				if tt.name == "sending only" {
					for _, want := range []string{"include:external.example", "all sender IPs pass", "receiving disabled"} {
						if !strings.Contains(stdout.String(), want) {
							t.Errorf("check detail missing %q: %s", want, stdout.String())
						}
					}
				}
			})
		}
	}
}

func TestDomainRecordsDispatch(t *testing.T) {
	pki := mintClientCert(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/domains/example.com/records", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, domainRecordsFixture)
	})
	mux.HandleFunc("POST /api/v1/domains/example.com/check", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, domainCheckFixture)
	})
	server, ca := newTLSServer(t, mux, pki.clientCAs)
	var stdout, stderr bytes.Buffer
	args := baseArgs(server, pki, ca, "--json", "domains", "records", "example.com")
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d; stderr = %s", code, stderr.String())
	}
	var report struct {
		Validation json.RawMessage `json:"validation"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	assertDomainJSON(t, report.Validation, domainCheckFixture)
}

func TestDomainUsage(t *testing.T) {
	for _, tt := range []struct {
		receiving bool
		want      string
	}{{false, "Sending only"}, {true, "Sending and receiving"}} {
		t.Run(tt.want, func(t *testing.T) {
			domain := domainResponse{Domain: "example.com", Status: "pending", ReceivingEnabled: tt.receiving}
			for name, render := range map[string]func(io.Writer){
				"list": func(out io.Writer) { renderDomains(out, []domainResponse{domain}) },
				"get":  func(out io.Writer) { renderDomain(out, &domain) },
			} {
				var out bytes.Buffer
				render(&out)
				if !strings.Contains(out.String(), tt.want) || !strings.Contains(out.String(), "pending") {
					t.Errorf("%s output = %q, want usage %q and independent pending status", name, out.String(), tt.want)
				}
			}
		})
	}
}
