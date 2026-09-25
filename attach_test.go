package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// attachRailHandler serves the two rail-side endpoints resolveAttachFiles and
// runSend touch: POST /api/v1/upload-grants and POST /api/v1/send. Each call
// is counted; captureBodyHandler does the decode-and-reply.
func attachRailHandler(grantCalls, sendCalls *int, capturedGrant, capturedSend *map[string]any, uploadURL string) http.HandlerFunc {
	grantHandler := captureBodyHandler(capturedGrant, http.StatusCreated, map[string]any{
		"upload_url": uploadURL, "expires_at": "2025-01-01T00:00:00Z",
		"max_files": 1, "max_bytes": 1,
	})
	sendHandler := captureBodyHandler(capturedSend, http.StatusAccepted, map[string]any{
		"id": "01msg", "request_id": "req-1", "recipients": 1, "status": "queued",
	})
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/upload-grants":
			*grantCalls++
			grantHandler(w, r)
		case "/api/v1/send":
			*sendCalls++
			sendHandler(w, r)
		default:
			http.NotFound(w, r)
		}
	}
}

// attachDepotHandler serves the public, unauthenticated grant-upload
// endpoint. On success it answers 201 with the "filename" query param as the
// id, so a test can tell which upload produced which id without a real depot.
func attachDepotHandler(uploadCalls *int, failStatus int, failBody map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*uploadCalls++
		if failStatus != 0 {
			jsonHandler(failStatus, failBody)(w, r)
			return
		}
		jsonHandler(http.StatusCreated, map[string]any{"id": r.URL.Query().Get("filename")})(w, r)
	}
}

// writeAttachFixture creates a file under t.TempDir() with the given content
// and returns its path.
func writeAttachFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return path
}

// attachSendArgs builds a `send` command line against the given server, with
// a fixed --from/--to/--subject/--text baseline plus extra flags.
func attachSendArgs(url string, pki testPKI, caFile string, extra ...string) []string {
	args := baseArgs(url, pki, caFile, "send",
		"--from", "a@x.com", "--to", "b@x.com", "--subject", "hi", "--text", "hello")
	return append(args, extra...)
}

// attachEnv is the common --attach-file test rig: a mock rail server (grant
// mint + send) wired to a mock depot upload server that always succeeds,
// with call counts and decoded request bodies to assert against.
type attachEnv struct {
	t                                  *testing.T
	pki                                testPKI
	url, caFile                        string
	grantCalls, sendCalls, uploadCalls int
	capturedGrant, capturedSend        map[string]any
}

func newAttachEnv(t *testing.T) *attachEnv {
	t.Helper()
	env := &attachEnv{t: t, pki: mintClientCert(t)}
	depotSrv := httptest.NewServer(attachDepotHandler(&env.uploadCalls, 0, nil))
	t.Cleanup(depotSrv.Close)
	env.url, env.caFile = newTLSServer(t,
		attachRailHandler(&env.grantCalls, &env.sendCalls, &env.capturedGrant, &env.capturedSend, depotSrv.URL+"/u/tok1"),
		env.pki.clientCAs)
	return env
}

// run invokes `wagon send` against env's mock rail server with extra flags
// added to the fixed baseline, --json first when jsonOut is set.
func (env *attachEnv) run(jsonOut bool, extra ...string) (stdout, stderr bytes.Buffer, exit int) {
	env.t.Helper()
	args := attachSendArgs(env.url, env.pki, env.caFile, extra...)
	if jsonOut {
		args = append([]string{"--json"}, args...)
	}
	exit = run(args, &stdout, &stderr)
	return stdout, stderr, exit
}

// TestSendAttachFile proves --attach-file mints one grant sized to the
// largest file, uploads each in flag order, and sends the resulting ids as
// attachments in that same order.
func TestSendAttachFile(t *testing.T) {
	env := newAttachEnv(t)
	fileA := writeAttachFixture(t, "a.txt", "hello A")
	fileB := writeAttachFixture(t, "b.txt", "hello world, this one is B and longer")

	stdout, stderr, got := env.run(false, "--attach-file", fileA, "--attach-file", fileB)
	if got != 0 {
		t.Fatalf("run(send --attach-file) exit = %d, want 0; stderr = %s", got, stderr.String())
	}

	if env.grantCalls != 1 {
		t.Errorf("grant mint calls = %d, want 1", env.grantCalls)
	}
	if env.uploadCalls != 2 {
		t.Errorf("depot upload calls = %d, want 2", env.uploadCalls)
	}
	if env.sendCalls != 1 {
		t.Errorf("send calls = %d, want 1", env.sendCalls)
	}

	if got := env.capturedGrant["max_files"]; got != float64(2) {
		t.Errorf("grant max_files = %v, want 2", got)
	}
	wantMaxBytes := float64(len("hello world, this one is B and longer"))
	if got := env.capturedGrant["max_bytes"]; got != wantMaxBytes {
		t.Errorf("grant max_bytes = %v, want %v (no headroom past the largest file's exact size)", got, wantMaxBytes)
	}
	if got := env.capturedGrant["file_ttl"]; got != attachFileTTL {
		t.Errorf("grant file_ttl = %v, want %q", got, attachFileTTL)
	}
	if _, ok := env.capturedGrant["content_types"]; ok {
		t.Errorf("grant request carries content_types, want it omitted: %v", env.capturedGrant)
	}
	if _, ok := env.capturedGrant["origins"]; ok {
		t.Errorf("grant request carries origins, want it omitted: %v", env.capturedGrant)
	}

	wantAttachments := []any{"a.txt", "b.txt"}
	gotAttachments, _ := env.capturedSend["attachments"].([]any)
	if len(gotAttachments) != len(wantAttachments) {
		t.Fatalf("send attachments = %v, want %v", gotAttachments, wantAttachments)
	}
	for i, want := range wantAttachments {
		if gotAttachments[i] != want {
			t.Errorf("attachments[%d] = %v, want %v", i, gotAttachments[i], want)
		}
	}

	if !bytes.Contains(stdout.Bytes(), []byte("uploaded "+fileA)) || !bytes.Contains(stdout.Bytes(), []byte("uploaded "+fileB)) {
		t.Errorf("stdout = %q, want an upload progress line per file", stdout.String())
	}
}

// TestSendAttachFileBadPath proves an unreadable path and a directory are
// both refused as a usage error before any network call.
func TestSendAttachFileBadPath(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{"missing file", func(t *testing.T) string { return filepath.Join(t.TempDir(), "does-not-exist.txt") }},
		{"directory", func(t *testing.T) string { return t.TempDir() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newAttachEnv(t)
			path := tt.path(t)

			_, stderr, got := env.run(false, "--attach-file", path)
			if got != 1 {
				t.Fatalf("run(send --attach-file) exit = %d, want 1; stderr = %s", got, stderr.String())
			}
			if !bytes.Contains(stderr.Bytes(), []byte(path)) {
				t.Errorf("stderr = %q, want it to name the path", stderr.String())
			}
			if env.grantCalls != 0 || env.uploadCalls != 0 || env.sendCalls != 0 {
				t.Errorf("calls: grant=%d upload=%d send=%d, want all 0", env.grantCalls, env.uploadCalls, env.sendCalls)
			}
		})
	}
}

// TestSendAttachFileUploadFailureAbortsSend proves a failed upload -- depot
// refusing the file -- aborts before the send is ever attempted.
func TestSendAttachFileUploadFailureAbortsSend(t *testing.T) {
	pki := mintClientCert(t)

	var grantCalls, sendCalls, uploadCalls int
	var capturedGrant, capturedSend map[string]any
	depot := httptest.NewServer(attachDepotHandler(&uploadCalls, http.StatusUnprocessableEntity,
		map[string]any{"error": "infected", "message": "malware detected"}))
	t.Cleanup(depot.Close)
	url, caFile := newTLSServer(t, attachRailHandler(&grantCalls, &sendCalls, &capturedGrant, &capturedSend, depot.URL+"/u/tok1"), pki.clientCAs)

	fileA := writeAttachFixture(t, "a.txt", "hello A")
	var stdout, stderr bytes.Buffer
	got := run(attachSendArgs(url, pki, caFile, "--attach-file", fileA), &stdout, &stderr)
	if got == 0 {
		t.Fatalf("run(send --attach-file, upload refused) exit = 0, want non-zero; stdout = %s", stdout.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("infected")) {
		t.Errorf("stderr = %q, want depot's error surfaced", stderr.String())
	}
	if grantCalls != 1 {
		t.Errorf("grant calls = %d, want 1 (mint happens before upload)", grantCalls)
	}
	if sendCalls != 0 {
		t.Errorf("send calls = %d, want 0: a failed upload must abort before the send", sendCalls)
	}
}

// TestSendAttachmentFlagRemoved proves --attachment is no longer a valid
// flag: wagon exits 1 (unknown flag) before any network call.
func TestSendAttachmentFlagRemoved(t *testing.T) {
	env := newAttachEnv(t)

	_, stderr, got := env.run(false, "--attachment", "some-id")
	if got != 1 {
		t.Fatalf("run(send --attachment) exit = %d, want 1 (unknown flag); stderr = %s", got, stderr.String())
	}
	if env.grantCalls != 0 || env.uploadCalls != 0 || env.sendCalls != 0 {
		t.Errorf("calls: grant=%d upload=%d send=%d, want all 0: an unknown flag must abort before any request", env.grantCalls, env.uploadCalls, env.sendCalls)
	}
}

// TestSendAttachFileJSONHasNoProgressLines proves --json emits only the send
// response: the upload progress lines are gated on !c.jsonOut specifically
// because this path exists and must stay silent.
func TestSendAttachFileJSONHasNoProgressLines(t *testing.T) {
	env := newAttachEnv(t)
	fileA := writeAttachFixture(t, "a.txt", "hello A")

	stdout, stderr, got := env.run(true, "--attach-file", fileA)
	if got != 0 {
		t.Fatalf("run(--json send --attach-file) exit = %d, want 0; stderr = %s", got, stderr.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte("uploaded ")) {
		t.Errorf("stdout = %q, want no upload progress lines under --json", stdout.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("--json stdout is not valid JSON: %v\nstdout = %s", err, stdout.String())
	}
	if resp["id"] != "01msg" {
		t.Errorf("json response = %v, want it to be the send response (id 01msg)", resp)
	}
}

// TestSendAttachFileMintFailureAbortsUpload proves a refused grant mint never
// reaches depot: the upload endpoint sees zero calls.
func TestSendAttachFileMintFailureAbortsUpload(t *testing.T) {
	pki := mintClientCert(t)

	var uploadCalls int
	depot := httptest.NewServer(attachDepotHandler(&uploadCalls, 0, nil))
	t.Cleanup(depot.Close)
	url, caFile := newTLSServer(t, jsonHandler(http.StatusServiceUnavailable,
		map[string]any{"error": "depot_unavailable", "message": "file uploads are temporarily unavailable"}), pki.clientCAs)

	fileA := writeAttachFixture(t, "a.txt", "hello A")
	var stdout, stderr bytes.Buffer
	got := run(attachSendArgs(url, pki, caFile, "--attach-file", fileA), &stdout, &stderr)
	if got == 0 {
		t.Fatalf("run(send, mint refused) exit = 0, want non-zero")
	}
	if uploadCalls != 0 {
		t.Errorf("depot upload calls = %d, want 0: a failed mint must never reach depot", uploadCalls)
	}
}
