package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"strings"
	"testing"
)

const verificationFixture = `{"id":"verification-1","email":"person@example.com","from":"agent@example.com","purpose":"signup","subject_id":"account-123","status":"pending","expires_at":"2026-11-14T12:10:00Z","resend_at":"2026-11-14T12:01:00Z"}`

const emailCheckFixture = `{"email":"person@example.com","status":"unknown","reason":"no_delivery_evidence","domain":{"has_mx":true,"routing":"mx","cached":false,"classification":{"category":"custom","traits":{"free":null,"disposable":false},"source":"routing_fallback","dataset_version":"test"}},"smtp":{"result":"unknown","reason":"probe_disabled","recipient_rejected":null,"checked_at":null,"next_probe_at":null},"evidence":{"source":"none","last_delivered_at":null},"future_field":true}`

func TestEmailAPICommands(t *testing.T) {
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
			name: "check", args: []string{"emails", "check", "person@example.com"},
			method: http.MethodPost, uri: "/api/v1/emails/check", body: `{"email":"person@example.com"}`,
			status: 200, response: emailCheckFixture,
			output: []string{"person@example.com", "no_delivery_evidence", "probe_disabled", "custom", "recipient_rejected:  unknown"},
		},
		{
			name: "create", args: []string{"email-verifications", "create", "--email", "person@example.com", "--purpose", "signup", "--subject-id", "account-123", "--request-id", "signup-123"},
			method: http.MethodPost, uri: "/api/v1/email-verifications",
			body:   `{"email":"person@example.com","from":"agent@example.com","purpose":"signup","subject_id":"account-123","request_id":"signup-123"}`,
			status: 202, response: verificationFixture,
			output: []string{"verification-1", "person@example.com", "pending", "verified_at:  -"},
		},
		{
			name: "replay", args: []string{"email-verifications", "create", "--email", "person@example.com", "--from", "other@nar.cy", "--purpose", "signup", "--subject-id", "account-123", "--request-id", "signup-123"},
			method: http.MethodPost, uri: "/api/v1/email-verifications",
			body:   `{"email":"person@example.com","from":"other@nar.cy","purpose":"signup","subject_id":"account-123","request_id":"signup-123"}`,
			status: 200, response: verificationFixture, output: []string{"pending"},
		},
		{
			name: "get escapes ID", args: []string{"email-verifications", "get", "id/part?x#y"},
			method: http.MethodGet, uri: "/api/v1/email-verifications/id%2Fpart%3Fx%23y",
			status: 200, response: verificationFixture, output: []string{"pending", "2026-11-14T12:10:00Z"},
		},
		{
			name: "confirm preserves leading zero", args: []string{"email-verifications", "confirm", "id/part?x#y", "--code", "012345"},
			method: http.MethodPost, uri: "/api/v1/email-verifications/id%2Fpart%3Fx%23y/confirm", body: `{"code":"012345"}`,
			status: 200, response: strings.Replace(verificationFixture[:len(verificationFixture)-1], `"pending"`, `"verified"`, 1) + `,"verified_at":"2026-11-14T12:02:00Z"}`,
			output: []string{"verified", "2026-11-14T12:02:00Z"},
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
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					if tt.body == "" {
						if len(body) != 0 {
							t.Errorf("unexpected body: %s", body)
						}
					} else {
						assertDomainJSON(t, body, tt.body)
						if r.Header.Get("Content-Type") != "application/json" {
							t.Error("request body must be JSON")
						}
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.response)
				}), pki.clientCAs)
				args := baseArgs(server, pki, ca)
				if jsonOut {
					args = append(args, "--json")
				}
				var stdout, stderr bytes.Buffer
				if code := run(append(args, tt.args...), &stdout, &stderr); code != 0 {
					t.Fatalf("exit = %d, want 0; stderr = %s", code, stderr.String())
				}
				if calls != 1 {
					t.Errorf("requests = %d, want 1", calls)
				}
				if jsonOut {
					assertDomainJSON(t, stdout.Bytes(), tt.response)
					return
				}
				for _, want := range tt.output {
					if !strings.Contains(stdout.String(), want) {
						t.Errorf("output = %s, want %q", stdout.String(), want)
					}
				}
			})
		}
	}
}

func TestEmailAPIErrors(t *testing.T) {
	pki := mintClientCert(t)
	tests := []struct {
		name   string
		args   []string
		status int
		body   string
		code   int
	}{
		{"check invalid address", []string{"emails", "check", "bad"}, 400, `{"error":"invalid_address","message":"email must be one bare mailbox address"}`, 3},
		{"check disabled", []string{"emails", "check", "person@example.com"}, 503, `{"error":"feature_disabled","message":"email checks are disabled"}`, 4},
		{"create forbidden", []string{"email-verifications", "create", "--email", "person@example.com", "--purpose", "signup", "--subject-id", "account-123"}, 403, `{"error":"sender_not_authorized","message":"from address not permitted"}`, 2},
		{"create conflict", []string{"email-verifications", "create", "--email", "person@example.com", "--purpose", "signup", "--subject-id", "account-123", "--request-id", "signup-123"}, 409, `{"error":"request_conflict","message":"request_id was already used"}`, 3},
		{"get missing", []string{"email-verifications", "get", "id"}, 404, `{"error":"not_found","message":"email verification not found"}`, 3},
		{"get expired certificate", []string{"email-verifications", "get", "id"}, 401, `{"error":"unauthorized","message":"client certificate expired"}`, 2},
		{"confirm wrong code", []string{"email-verifications", "confirm", "id", "--code", "012345"}, 400, `{"error":"invalid_code","message":"verification code is invalid or no longer usable"}`, 3},
		{"confirm limited", []string{"email-verifications", "confirm", "id", "--code", "012345"}, 429, `{"error":"rate_limited","message":"email verification rate limit exceeded"}`, 3},
		{"get malformed response", []string{"email-verifications", "get", "id"}, 200, `{`, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server, ca := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}), pki.clientCAs)
			var stdout, stderr bytes.Buffer
			if code := run(baseArgs(server, pki, ca, tt.args...), &stdout, &stderr); code != tt.code {
				t.Errorf("exit = %d, want %d; stderr = %s", code, tt.code, stderr.String())
			}
			if calls != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Errorf("requests = %d, stdout = %s, stderr = %s; want one request and only an error", calls, stdout.String(), stderr.String())
			}
		})
	}
}

func TestEmailVerificationMissingSender(t *testing.T) {
	err := runEmailVerifications(&client{}, []string{"create", "--email", "person@example.com", "--purpose", "signup", "--subject-id", "account-123"}, io.Discard, io.Discard)
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "--from is required") {
		t.Errorf("error = %v, want missing sender usage error", err)
	}
}

func TestEmailVerificationGeneratedRequestID(t *testing.T) {
	pki := mintClientCert(t)
	var ids []string
	server, ca := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req["request_id"] == "" || req["from"] != "agent@example.com" {
			t.Errorf("request defaults = %v", req)
		}
		ids = append(ids, req["request_id"])
		_, _ = io.WriteString(w, verificationFixture)
	}), pki.clientCAs)
	for range 2 {
		var stderr bytes.Buffer
		args := baseArgs(server, pki, ca, "email-verifications", "create", "--email", "person@example.com", "--purpose", "signup", "--subject-id", "account-123")
		if code := run(args, io.Discard, &stderr); code != 0 {
			t.Fatalf("exit = %d, want 0; stderr = %s", code, stderr.String())
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Errorf("request IDs = %v, want two distinct IDs", ids)
	}
}

func TestEmailCheckAssessments(t *testing.T) {
	pki := mintClientCert(t)
	tests := []struct {
		status   string
		rejected string
		want     string
	}{
		{"valid", "false", "false"},
		{"invalid", "true", "true"},
		{"undeliverable", "true", "true"},
		{"unknown", "null", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			response := strings.Replace(emailCheckFixture, `"status":"unknown"`, `"status":"`+tt.status+`"`, 1)
			response = strings.Replace(response, `"recipient_rejected":null`, `"recipient_rejected":`+tt.rejected, 1)
			server, ca := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, response)
			}), pki.clientCAs)
			var stdout, stderr bytes.Buffer
			if code := run(baseArgs(server, pki, ca, "emails", "check", "person@example.com"), &stdout, &stderr); code != 0 {
				t.Fatalf("exit = %d, want 0; stderr = %s", code, stderr.String())
			}
			for _, want := range []string{"status:              " + tt.status, "recipient_rejected:  " + tt.want} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("output = %s, want %q", stdout.String(), want)
				}
			}
		})
	}
}

func TestEmailCheckProvisionalAcceptance(t *testing.T) {
	pki := mintClientCert(t)
	for _, tt := range []struct {
		name, status, reason, smtp string
		wantAssessment             bool
	}{
		{"provisional acceptance", "unknown", "recipient_accepted", "accepted", true},
		{"probe disabled", "unknown", "probe_disabled", "not_run", false},
		{"other unknown reason", "unknown", "no_delivery_evidence", "accepted", false},
		{"valid", "valid", "recipient_accepted", "accepted", false},
		{"invalid", "invalid", "recipient_accepted", "accepted", false},
		{"recipient not accepted", "unknown", "recipient_accepted", "rejected", false},
	} {
		for _, jsonOut := range []bool{false, true} {
			name := tt.name
			if jsonOut {
				name += " JSON"
			}
			t.Run(name, func(t *testing.T) {
				response := strings.NewReplacer(
					`"status":"unknown"`, `"status":"`+tt.status+`"`,
					`"reason":"no_delivery_evidence"`, `"reason":"`+tt.reason+`"`,
					`"result":"unknown"`, `"result":"`+tt.smtp+`"`,
					`"reason":"probe_disabled"`, `"reason":"`+tt.reason+`"`,
				).Replace(emailCheckFixture)
				server, ca := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, response)
				}), pki.clientCAs)
				args := baseArgs(server, pki, ca)
				if jsonOut {
					args = append(args, "--json")
				}
				var stdout, stderr bytes.Buffer
				if code := run(append(args, "emails", "check", "person@example.com"), &stdout, &stderr); code != 0 {
					t.Fatalf("exit = %d, want 0; stderr = %s", code, stderr.String())
				}
				if jsonOut {
					assertDomainJSON(t, stdout.Bytes(), response)
					return
				}
				output := stdout.String()
				assessment := "SMTP accepted the recipient; mailbox existence is unconfirmed."
				if got := strings.Contains(output, "assessment:"); got != tt.wantAssessment {
					t.Errorf("output = %s, assessment row = %v, want %v", output, got, tt.wantAssessment)
				}
				if got := strings.Contains(output, assessment); got != tt.wantAssessment {
					t.Errorf("output = %s, provisional explanation = %v, want %v", output, got, tt.wantAssessment)
				}
				for _, want := range []string{"status:              " + tt.status, "reason:              " + tt.reason, "smtp_result:         " + tt.smtp} {
					if !strings.Contains(output, want) {
						t.Errorf("output = %s, want %q", output, want)
					}
				}
				if tt.wantAssessment && !strings.Contains(output, "reason:              "+tt.reason+"\nassessment:") {
					t.Errorf("output = %s, want assessment immediately after reason", output)
				}
			})
		}
	}
}

func TestEmailAPIUsage(t *testing.T) {
	tests := [][]string{
		{"emails"}, {"emails", "bogus"}, {"emails", "check"}, {"emails", "check", ""},
		{"emails", "check", "person@example.com", "extra"}, {"emails", "check", "--email", "person@example.com"},
		{"email-verifications"}, {"email-verifications", "bogus"}, {"email-verifications", "create"},
		{"email-verifications", "create", "--email", "person@example.com"},
		{"email-verifications", "create", "--email", "person@example.com", "--purpose", "signup"},
		{"email-verifications", "create", "--email", "person@example.com", "--purpose", "signup", "--subject-id", "account-123", "extra"},
		{"email-verifications", "create", "--bogus"},
		{"email-verifications", "get"}, {"email-verifications", "get", ""},
		{"email-verifications", "get", "id", "extra"}, {"email-verifications", "get", "id", "--bogus"},
		{"email-verifications", "confirm"}, {"email-verifications", "confirm", "id"},
		{"email-verifications", "confirm", "id", "--code", "12345"},
		{"email-verifications", "confirm", "id", "--code", "1234567"},
		{"email-verifications", "confirm", "id", "--code", "abcdef"},
		{"email-verifications", "confirm", "id", "--code", "１２"},
		{"email-verifications", "confirm", "--code", "012345", "id"},
		{"email-verifications", "confirm", "id", "--code", "012345", "extra"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			err := dispatch(nil, args[0], args[1:], io.Discard, io.Discard)
			if _, ok := errors.AsType[*usageError](err); !ok {
				t.Fatalf("error = %v, want usage error", err)
			}
		})
	}
	for _, args := range [][]string{
		{"emails", "check", "--help"}, {"email-verifications", "create", "--help"},
		{"email-verifications", "get", "--help"}, {"email-verifications", "confirm", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := dispatch(nil, args[0], args[1:], io.Discard, io.Discard); !errors.Is(err, flag.ErrHelp) {
				t.Errorf("error = %v, want help", err)
			}
		})
	}
}
