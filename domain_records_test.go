package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type fakeDomainResolver struct {
	txt    map[string][]string
	mx     map[string][]*net.MX
	ns     map[string][]*net.NS
	errors map[string]error
	calls  map[string]int
	before func(context.Context, string)
}

func (r *fakeDomainResolver) query(ctx context.Context, name, kind string) error {
	key := kind + " " + name
	if r.calls == nil {
		r.calls = make(map[string]int)
	}
	r.calls[key]++
	if r.before != nil {
		r.before(ctx, key)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.errors[key]
}

func (r *fakeDomainResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return r.txt[name], r.query(ctx, name, "TXT")
}

func (r *fakeDomainResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return r.mx[name], r.query(ctx, name, "MX")
}

func (r *fakeDomainResolver) LookupNS(ctx context.Context, name string) ([]*net.NS, error) {
	return r.ns[name], r.query(ctx, name, "NS")
}

func TestValidateDomainRecords(t *testing.T) {
	tests := []struct {
		name     string
		record   domainDNSRecord
		resolver fakeDomainResolver
		status   string
		observed []string
	}{
		{
			name: "ownership TXT among unrelated records", record: domainDNSRecord{"EXAMPLE.com", "TXT", "rail-verify=proof"},
			resolver: fakeDomainResolver{txt: map[string][]string{"example.com.": {"unrelated", "rail-verify=proof"}}},
			status:   "match", observed: []string{"rail-verify=proof", "unrelated"},
		},
		{
			name: "TXT case matters", record: domainDNSRecord{"example.com", "TXT", "rail-verify=Proof"},
			resolver: fakeDomainResolver{txt: map[string][]string{"example.com.": {"rail-verify=proof"}}},
			status:   "mismatch", observed: []string{"rail-verify=proof"},
		},
		{
			name: "SPF mismatch", record: domainDNSRecord{"example.com", "TXT", "v=spf1 include:smtp.example.com ~all"},
			resolver: fakeDomainResolver{txt: map[string][]string{"example.com.": {"v=spf1 -all"}}},
			status:   "mismatch", observed: []string{"v=spf1 -all"},
		},
		{
			name: "DMARC match", record: domainDNSRecord{"_dmarc.example.com", "TXT", "v=DMARC1; p=reject"},
			resolver: fakeDomainResolver{txt: map[string][]string{"_dmarc.example.com.": {"v=DMARC1; p=reject"}}},
			status:   "match", observed: []string{"v=DMARC1; p=reject"},
		},
		{
			name: "DKIM value joined by resolver", record: domainDNSRecord{"railrsa._domainkey.example.com", "TXT", "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 400)},
			resolver: fakeDomainResolver{txt: map[string][]string{"railrsa._domainkey.example.com.": {"v=DKIM1; k=rsa; p=" + strings.Repeat("A", 400)}}},
			status:   "match", observed: []string{"v=DKIM1; k=rsa; p=" + strings.Repeat("A", 400)},
		},
		{
			name: "separate TXT records are not concatenated", record: domainDNSRecord{"example.com", "TXT", "firstsecond"},
			resolver: fakeDomainResolver{txt: map[string][]string{"example.com.": {"first", "second"}}},
			status:   "mismatch", observed: []string{"first", "second"},
		},
		{
			name: "MX normalizes hostname", record: domainDNSRecord{"example.com.", "MX", "10 SMTP.EXAMPLE.COM"},
			resolver: fakeDomainResolver{mx: map[string][]*net.MX{"example.com.": {{Host: "smtp.example.com.", Pref: 10}}}},
			status:   "match", observed: []string{"10 smtp.example.com."},
		},
		{
			name: "MX priority matters", record: domainDNSRecord{"example.com", "MX", "20 smtp.example.com."},
			resolver: fakeDomainResolver{mx: map[string][]*net.MX{"example.com.": {{Host: "smtp.example.com.", Pref: 10}}}},
			status:   "mismatch", observed: []string{"10 smtp.example.com."},
		},
		{
			name: "NS normalizes hostname", record: domainDNSRecord{"example.com", "NS", "NS1.EXAMPLE.NET."},
			resolver: fakeDomainResolver{ns: map[string][]*net.NS{"example.com.": {{Host: "ns1.example.net."}, {Host: "ns2.example.net."}}}},
			status:   "match", observed: []string{"ns1.example.net.", "ns2.example.net."},
		},
		{
			name: "no answers", record: domainDNSRecord{"example.com", "TXT", "expected"},
			status: "missing", observed: []string{},
		},
		{
			name: "NXDOMAIN", record: domainDNSRecord{"example.com", "TXT", "expected"},
			resolver: fakeDomainResolver{errors: map[string]error{"TXT example.com.": &net.DNSError{Err: "no such host", IsNotFound: true}}},
			status:   "missing", observed: []string{},
		},
		{
			name: "SERVFAIL is not missing", record: domainDNSRecord{"example.com", "TXT", "expected"},
			resolver: fakeDomainResolver{errors: map[string]error{"TXT example.com.": &net.DNSError{Err: "server misbehaving", IsTemporary: true}}},
			status:   "error", observed: []string{},
		},
		{
			name: "partial answer with error cannot pass", record: domainDNSRecord{"example.com", "TXT", "expected"},
			resolver: fakeDomainResolver{txt: map[string][]string{"example.com.": {"expected"}}, errors: map[string]error{"TXT example.com.": &net.DNSError{Err: "timeout", IsTimeout: true}}},
			status:   "error", observed: []string{"expected"},
		},
		{
			name: "unsupported type", record: domainDNSRecord{"example.com", "A", "192.0.2.1"},
			status: "error", observed: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := validateDomainRecords(&tt.resolver, []domainDNSRecord{tt.record})
			if len(results) != 1 {
				t.Fatalf("got %d results, want 1", len(results))
			}
			got := results[0]
			if got.Status != tt.status || !slices.Equal(got.Observed, tt.observed) {
				t.Errorf("result = %+v, want %s with %q", got, tt.status, tt.observed)
			}
			if (got.Error != "") != (tt.status == "error") {
				t.Errorf("error = %q for status %s", got.Error, tt.status)
			}
			if got.Name != tt.record.Name || got.Type != tt.record.Type || got.Expected != tt.record.Value {
				t.Errorf("expected record changed: %+v", got)
			}
		})
	}
}

func TestDomainRecordsReuseDNSAnswer(t *testing.T) {
	resolver := &fakeDomainResolver{ns: map[string][]*net.NS{"example.com.": {{Host: "ns1.example.net."}, {Host: "ns2.example.net."}}}}
	results := validateDomainRecords(resolver, []domainDNSRecord{
		{"EXAMPLE.COM", "NS", "ns1.example.net."},
		{"example.com.", "NS", "ns2.example.net."},
	})
	if resolver.calls["NS example.com."] != 1 || len(resolver.calls) != 1 {
		t.Errorf("calls = %v, want one NS lookup", resolver.calls)
	}
	for _, result := range results {
		if result.Status != "match" {
			t.Errorf("result = %+v, want match", result)
		}
	}
}

func TestDomainRecordsTimeoutContinues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resolver := &fakeDomainResolver{
			txt: map[string][]string{"second.example.": {"second"}},
			before: func(ctx context.Context, key string) {
				if key == "TXT first.example." {
					<-ctx.Done()
				}
			},
		}
		start := time.Now()
		results := validateDomainRecords(resolver, []domainDNSRecord{
			{"first.example", "TXT", "first"}, {"second.example", "TXT", "second"},
		})
		if elapsed := time.Since(start); elapsed != domainDNSTimeout {
			t.Errorf("elapsed = %s, want %s", elapsed, domainDNSTimeout)
		}
		if results[0].Status != "error" || results[1].Status != "match" {
			t.Errorf("timeout must not skip later records: %+v", results)
		}
	})
}

func TestRunDomainRecords(t *testing.T) {
	pki := mintClientCert(t)
	const response = `{"records":[{"name":"example.com","type":"MX","value":"10 smtp.example.com."},{"name":"_rail.example.com","type":"TXT","value":"proof"}],"future_field":9007199254740993}`
	tests := []struct {
		name     string
		response string
		resolver fakeDomainResolver
		wantCode int
		statuses []string
	}{
		{
			name: "matches", response: response,
			resolver: fakeDomainResolver{mx: map[string][]*net.MX{"example.com.": {{Host: "smtp.example.com.", Pref: 10}}}, txt: map[string][]string{"_rail.example.com.": {"proof"}}},
			statuses: []string{"match", "match"},
		},
		{
			name: "mismatch and missing", response: response,
			resolver: fakeDomainResolver{mx: map[string][]*net.MX{"example.com.": {{Host: "other.example.com.", Pref: 10}}}},
			wantCode: 5, statuses: []string{"mismatch", "missing"},
		},
		{
			name: "lookup error and mismatch", response: response,
			resolver: fakeDomainResolver{errors: map[string]error{"MX example.com.": &net.DNSError{Err: "server misbehaving"}}, txt: map[string][]string{"_rail.example.com.": {"different"}}},
			wantCode: 4, statuses: []string{"error", "mismatch"},
		},
		{name: "empty", response: `{"records":[]}`, statuses: []string{}},
	}
	for _, tt := range tests {
		for _, jsonOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tt.name, jsonOut), func(t *testing.T) {
				server, ca := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.RequestURI != "/api/v1/domains/example.com/records" {
						t.Errorf("unexpected request: %s %s", r.Method, r.RequestURI)
					}
					_, _ = io.WriteString(w, tt.response)
				}), pki.clientCAs)
				c, err := newClient(server, pki.certFile, pki.keyFile, ca, jsonOut)
				if err != nil {
					t.Fatal(err)
				}
				var stdout bytes.Buffer
				resolver := tt.resolver
				resolver.before = func(_ context.Context, _ string) {
					if !jsonOut && !strings.Contains(stdout.String(), "proof") {
						t.Error("expected records must be printed before DNS lookup")
					}
					if jsonOut && stdout.Len() != 0 {
						t.Error("JSON must be one complete document")
					}
				}
				if err := runDomainRecords(c, "example.com", &stdout, &resolver); exitCode(err) != tt.wantCode {
					t.Errorf("exit = %d, want %d; error = %v", exitCode(err), tt.wantCode, err)
				}
				if jsonOut {
					var got struct {
						Records    json.RawMessage          `json:"records"`
						Validation []domainRecordValidation `json:"validation"`
						Future     json.RawMessage          `json:"future_field"`
					}
					if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if len(tt.statuses) > 0 && string(got.Future) != "9007199254740993" {
						t.Error("original API fields were not preserved")
					}
					if len(got.Validation) != len(tt.statuses) {
						t.Fatalf("got %d results, want %d", len(got.Validation), len(tt.statuses))
					}
					for i, status := range tt.statuses {
						if got.Validation[i].Status != status {
							t.Errorf("result %d = %+v, want %s", i, got.Validation[i], status)
						}
					}
					if len(tt.statuses) == 0 && got.Validation == nil {
						t.Error("empty results must be [], not null")
					}
					return
				}
				for _, want := range append([]string{"Expected DNS records:", "DNS validation (system resolver):"}, tt.statuses...) {
					if !strings.Contains(stdout.String(), want) {
						t.Errorf("output missing %q: %s", want, stdout.String())
					}
				}
			})
		}
	}
}

func TestDomainRecordsDispatch(t *testing.T) {
	pki := mintClientCert(t)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/domains/example.com/records", jsonHandler(http.StatusOK, map[string]any{"records": []any{}}))
	server, ca := newTLSServer(t, mux, pki.clientCAs)
	var stdout, stderr bytes.Buffer
	args := baseArgs(server, pki, ca, "--json", "domains", "records", "example.com")
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d; stderr = %s", code, stderr.String())
	}
	assertDomainJSON(t, stdout.Bytes(), `{"records":[],"validation":[]}`)
}
