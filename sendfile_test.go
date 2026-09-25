package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// runSendFile invokes `wagon send-file` against env's mock rail server.
func (env *attachEnv) runSendFile(jsonOut bool, args ...string) (stdout, stderr bytes.Buffer, exit int) {
	env.t.Helper()
	full := baseArgs(env.url, env.pki, env.caFile, append([]string{"send-file"}, args...)...)
	if jsonOut {
		full = append([]string{"--json"}, full...)
	}
	exit = run(full, &stdout, &stderr)
	return stdout, stderr, exit
}

func TestSendFile(t *testing.T) {
	tests := []struct {
		name  string
		flags []string
		files []string
		want  map[string]any
	}{
		{
			name:  "one file takes every default",
			flags: []string{"--to", "b@x.com"},
			files: []string{"a.txt"},
			want: map[string]any{
				"from":        "agent@example.com",
				"to":          []any{"b@x.com"},
				"subject":     "a.txt",
				"body_text":   "Attached: a.txt",
				"attachments": []any{"a.txt"},
			},
		},
		{
			name:  "two files",
			flags: []string{"--to", "b@x.com"},
			files: []string{"a.txt", "b.csv"},
			want: map[string]any{
				"subject":     "2 files",
				"body_text":   "Attached: a.txt, b.csv",
				"attachments": []any{"a.txt", "b.csv"},
			},
		},
		{
			name: "flags override the defaults",
			flags: []string{"--from", "Agent <agent@example.com>", "--to", "b@x.com", "--cc", "c@x.com",
				"--subject", "Q3", "--text", "See attached.", "--request-id", "req-9"},
			files: []string{"a.txt"},
			want: map[string]any{
				"from":       "Agent <agent@example.com>",
				"cc":         []any{"c@x.com"},
				"subject":    "Q3",
				"body_text":  "See attached.",
				"request_id": "req-9",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newAttachEnv(t)
			args := tt.flags
			for _, name := range tt.files {
				args = append(args, writeAttachFixture(t, name, "content of "+name))
			}

			stdout, stderr, got := env.runSendFile(false, args...)
			if got != 0 {
				t.Fatalf("run(send-file) exit = %d, want 0; stderr = %s", got, stderr.String())
			}
			if env.grantCalls != 1 || env.uploadCalls != len(tt.files) || env.sendCalls != 1 {
				t.Errorf("calls: grant=%d upload=%d send=%d, want 1, %d, 1", env.grantCalls, env.uploadCalls, env.sendCalls, len(tt.files))
			}
			for k, v := range tt.want {
				gotJSON, _ := json.Marshal(env.capturedSend[k])
				wantJSON, _ := json.Marshal(v)
				if string(gotJSON) != string(wantJSON) {
					t.Errorf("send body field %q = %s, want %s", k, gotJSON, wantJSON)
				}
			}
			if !bytes.Contains(stdout.Bytes(), []byte("01msg")) {
				t.Errorf("stdout = %q, want it to contain the message id", stdout.String())
			}
		})
	}
}

// TestSendFileUsageErrors proves each bad command line exits 1 before any
// network call.
func TestSendFileUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args func(t *testing.T) []string
	}{
		{"no --to", func(t *testing.T) []string { return []string{writeAttachFixture(t, "a.txt", "A")} }},
		{"no files", func(t *testing.T) []string { return []string{"--to", "b@x.com"} }},
		{"missing file", func(t *testing.T) []string {
			return []string{"--to", "b@x.com", filepath.Join(t.TempDir(), "nope.txt")}
		}},
		{"directory", func(t *testing.T) []string { return []string{"--to", "b@x.com", t.TempDir()} }},
		{"flag after the file", func(t *testing.T) []string {
			return []string{writeAttachFixture(t, "a.txt", "A"), "--to", "b@x.com"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newAttachEnv(t)
			_, stderr, got := env.runSendFile(false, tt.args(t)...)
			if got != 1 {
				t.Fatalf("run(send-file) exit = %d, want 1; stderr = %s", got, stderr.String())
			}
			if env.grantCalls != 0 || env.uploadCalls != 0 || env.sendCalls != 0 {
				t.Errorf("calls: grant=%d upload=%d send=%d, want all 0", env.grantCalls, env.uploadCalls, env.sendCalls)
			}
		})
	}
}

func TestSendFileJSON(t *testing.T) {
	env := newAttachEnv(t)
	stdout, stderr, got := env.runSendFile(true, "--to", "b@x.com", writeAttachFixture(t, "a.txt", "A"))
	if got != 0 {
		t.Fatalf("run(--json send-file) exit = %d, want 0; stderr = %s", got, stderr.String())
	}
	if strings.Contains(stdout.String(), "uploaded ") {
		t.Errorf("stdout = %q, want no upload progress lines under --json", stdout.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("--json stdout is not valid JSON: %v\nstdout = %s", err, stdout.String())
	}
	if resp["id"] != "01msg" {
		t.Errorf("json response = %v, want the send response (id 01msg)", resp)
	}
}

// TestFromDefaultsToCertSender proves send and forms create fill a missing
// --from with the certificate's first sender.
func TestFromDefaultsToCertSender(t *testing.T) {
	tests := []struct {
		name string
		verb []string
	}{
		{"send", []string{"send", "--to", "b@x.com", "--subject", "hi", "--text", "hello"}},
		{"forms create", []string{"forms", "create", "--name", "contact", "--subject", "hi", "--to", "b@x.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pki := mintClientCertWithSenders(t, "first@example.com", "second@example.com")
			var captured map[string]any
			url, caFile := newTLSServer(t, captureBodyHandler(&captured, http.StatusCreated, map[string]any{"id": "01msg"}), pki.clientCAs)

			var stdout, stderr bytes.Buffer
			if got := run(baseArgs(url, pki, caFile, tt.verb...), &stdout, &stderr); got != 0 {
				t.Fatalf("run(%s) exit = %d, want 0; stderr = %s", tt.name, got, stderr.String())
			}
			if captured["from"] != "first@example.com" {
				t.Errorf("request from = %v, want first@example.com", captured["from"])
			}
		})
	}
}

// TestFromRequiredWithoutCertSender proves a certificate with no email SAN
// makes --from required, and nothing is sent.
func TestFromRequiredWithoutCertSender(t *testing.T) {
	pki := mintClientCertWithSenders(t)
	calls := 0
	url, caFile := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	got := run(baseArgs(url, pki, caFile, "send", "--to", "b@x.com", "--subject", "hi"), &stdout, &stderr)
	if got != 1 {
		t.Fatalf("run(send) exit = %d, want 1; stderr = %s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--from is required") {
		t.Errorf("stderr = %q, want it to ask for --from", stderr.String())
	}
	if calls != 0 {
		t.Errorf("server calls = %d, want 0", calls)
	}
}

// TestSenderNotAuthorizedListsCertSenders proves rail's sender rejection
// names the senders the certificate allows, and keeps exit code 2.
func TestSenderNotAuthorizedListsCertSenders(t *testing.T) {
	pki := mintClientCertWithSenders(t, "noreply@example.com", "hello@example.com")
	url, caFile := newTLSServer(t, jsonHandler(http.StatusForbidden, map[string]any{
		"error": "sender_not_authorized", "message": "from address not permitted by client certificate",
	}), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	got := run(baseArgs(url, pki, caFile, "send", "--from", "me@example.com", "--to", "b@x.com", "--subject", "hi"), &stdout, &stderr)
	if got != 2 {
		t.Fatalf("run(send) exit = %d, want 2; stderr = %s", got, stderr.String())
	}
	want := "sender_not_authorized: from address not permitted by client certificate (allowed: noreply@example.com, hello@example.com)"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
	}
}
