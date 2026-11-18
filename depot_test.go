package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func depotTestClient(t *testing.T, handler http.Handler, jsonOut bool) *client {
	t.Helper()
	pki := mintClientCert(t)
	server, ca := newTLSServer(t, handler, pki.clientCAs)
	c, err := newClient(server, pki.certFile, pki.keyFile, ca, jsonOut)
	if err != nil {
		t.Fatalf("create depot client: %v", err)
	}
	return c
}

func TestDepotOwnerCommands(t *testing.T) {
	file := map[string]any{
		"id": "file-1", "filename": "report.txt", "state": "available", "verdict": "clean",
		"size": 42, "servable": true, "exportable": true,
	}
	tests := []struct {
		name, method, uri string
		args              []string
		request           string
		status            int
		response          any
		wantHuman         string
	}{
		{"list", http.MethodGet, "/api/v1/files?before=other%2B%2F%3F%3D",
			[]string{"list", "--before", "other+/?="}, "", 200, map[string]any{"files": []any{file}, "next_before": "file-1"}, "next_before: file-1"},
		{"get", http.MethodGet, "/api/v1/files/file-1",
			[]string{"get", "file-1"}, "", 200, file, "report.txt"},
		{"progress", http.MethodGet, "/api/v1/files/file-1/progress",
			[]string{"progress", "file-1"}, "", 200,
			map[string]any{"id": "file-1", "received": 2, "total": 42, "state": "pending_upload"}, "pending_upload"},
		{"delete", http.MethodDelete, "/api/v1/files/file-1",
			[]string{"delete", "file-1"}, "", 204, nil, "deleted file file-1"},
		{"claim", http.MethodPost, "/api/v1/files/file-1/claim",
			[]string{"claim", "file-1", "--ttl", "24h"},
			`{"ttl":"24h"}`, 200, file, "report.txt"},
		{"grant", http.MethodPost, "/api/v1/upload-grants",
			[]string{"grant", "--ttl", "1m", "--max-files", "2", "--max-bytes", "42", "--origin", "https://one.example", "--origin", "https://two.example", "--file-ttl", "24h", "--content-type", "text/plain"},
			`{"ttl":"1m","file_ttl":"24h","max_files":2,"max_bytes":42,"origins":["https://one.example","https://two.example"],"content_types":["text/plain"]}`, 201,
			map[string]any{"upload_url": "https://depot.example/u/test-grant", "expires_at": "2026-12-01T12:15:00Z"}, "https://depot.example/u/test-grant"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			c := depotTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != tt.method || r.URL.RequestURI() != tt.uri {
					t.Errorf("request = %s %s, want %s %s", r.Method, r.URL.RequestURI(), tt.method, tt.uri)
				}
				if len(r.TLS.PeerCertificates) != 1 {
					t.Error("owner request has no client certificate")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != tt.request {
					t.Errorf("body = %q, err = %v, want %q", body, err, tt.request)
				}
				if tt.status == http.StatusNoContent {
					w.WriteHeader(tt.status)
					return
				}
				jsonHandler(tt.status, tt.response)(w, r)
			}), false)
			var out, stderr bytes.Buffer
			if err := runDepot(c, tt.args, &out, &stderr); err != nil || !strings.Contains(out.String(), tt.wantHuman) {
				t.Fatalf("depot %v: stdout = %q, err = %v, want %q", tt.args, &out, err, tt.wantHuman)
			}
			out.Reset()
			c.jsonOut = true
			if err := runDepot(c, tt.args, &out, &stderr); err != nil {
				t.Fatalf("depot --json %v: %v", tt.args, err)
			}
			if tt.status == http.StatusNoContent && out.Len() != 0 || tt.status != http.StatusNoContent && !json.Valid(out.Bytes()) {
				t.Errorf("depot --json %v: unexpected output %q", tt.args, &out)
			}
			if got := calls.Load(); got != 2 {
				t.Errorf("calls = %d, want 2", got)
			}
		})
	}
}

func TestDepotDownload(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		name := "file"
		if redirect {
			name = "redirect"
		}
		t.Run(name, func(t *testing.T) {
			const publicPath = "/d/synthetic-download?e=123&s=test"
			contents := []byte{0, 5, 255}
			var calls atomic.Int32
			public := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.RequestURI() != publicPath || r.Method != http.MethodGet {
					t.Error("unexpected public download request")
				}
				if len(r.TLS.PeerCertificates) != 0 {
					t.Error("public download presented a client certificate")
				}
				if redirect {
					http.Redirect(w, r, "/d/other-token", http.StatusTemporaryRedirect)
					return
				}
				_, _ = w.Write(contents)
			}))
			public.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequestClientCert}
			public.StartTLS()
			t.Cleanup(public.Close)
			var metadataCalls atomic.Int32
			c := depotTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				metadataCalls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/files/test" {
					t.Error("unexpected metadata request")
				}
				jsonHandler(200, map[string]any{"id": "test", "servable": true, "url": public.URL + publicPath})(w, r)
			}), false)

			var out bytes.Buffer
			err := runDepot(c, []string{"download", "test"}, &out, io.Discard)
			if redirect {
				if err == nil || strings.Contains(err.Error(), publicPath) || out.Len() != 0 {
					t.Errorf("redirect: err = %v, wrote %d bytes", err, out.Len())
				}
			} else if err != nil || !bytes.Equal(out.Bytes(), contents) {
				t.Errorf("download: err = %v, content = %v, want %v", err, out.Bytes(), contents)
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("public download calls = %d, want 1", got)
			}
			c.jsonOut = true
			if err := runDepot(c, []string{"download", "test"}, io.Discard, io.Discard); exitCode(err) != 1 {
				t.Errorf("JSON download: err = %v, want usage error", err)
			}
			if got := metadataCalls.Load(); got != 1 {
				t.Errorf("metadata calls = %d, want 1", got)
			}
		})
	}
}

func TestDepotOwnerRedirect(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	c := depotTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}), false)
	err := runDepot(c, []string{"delete", "file-1"}, io.Discard, io.Discard)
	if err == nil || redirected.Load() {
		t.Errorf("delete redirect: err = %v, target called = %v", err, redirected.Load())
	}
}

func depotTestFile(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test file+.txt")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write upload fixture: %v", err)
	}
	return path
}

func TestDepotUpload(t *testing.T) {
	path := depotTestFile(t, "abc")
	for _, resumable := range []bool{false, true} {
		name := "put"
		if resumable {
			name = "tus"
		}
		t.Run(name, func(t *testing.T) {
			const uploadID = "/api/v1/files/test"
			calls := make(chan string, 4)
			c := depotTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				calls <- r.Method
				switch r.Method {
				case http.MethodPut:
					if r.URL.Path != "/api/v1/files" || r.URL.Query().Get("filename") != filepath.Base(path) ||
						r.Header.Get("Content-Type") != "text/test" || string(body) != "abc" || r.ContentLength != 3 {
						t.Errorf("PUT: URI = %s, content-type = %s, length = %d, body = %q", r.URL.RequestURI(), r.Header.Get("Content-Type"), r.ContentLength, body)
					}
					jsonHandler(201, map[string]any{"id": "test"})(w, r)
				case http.MethodPost:
					if r.URL.Path != "/api/v1/files" || r.Header.Get("Tus-Resumable") != "1.0.0" ||
						r.Header.Get("Upload-Length") != "3" || len(body) != 0 ||
						r.Header.Get("Upload-Metadata") != "filename dGVzdCBmaWxlKy50eHQ=,filetype dGV4dC90ZXN0,wagon-sha256 ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=" {
						t.Errorf("POST: URI = %s, headers = %v, body = %q", r.URL.RequestURI(), r.Header, body)
					}
					w.Header().Set("Location", uploadID)
					w.Header().Set("Upload-Offset", "0")
					w.WriteHeader(201)
				case http.MethodPatch:
					if r.URL.Path != uploadID || r.Header.Get("Tus-Resumable") != "1.0.0" ||
						r.Header.Get("Upload-Offset") != "0" || r.Header.Get("Content-Type") != "application/offset+octet-stream" ||
						string(body) != "abc" || r.ContentLength != 3 {
						t.Errorf("PATCH: URI = %s, headers = %v, length = %d, body = %q", r.URL.RequestURI(), r.Header, r.ContentLength, body)
					}
					w.Header().Set("Upload-Offset", "3")
					w.WriteHeader(204)
				case http.MethodGet:
					jsonHandler(200, map[string]any{"id": "test", "state": "active", "servable": true})(w, r)
				default:
					t.Errorf("unexpected method %s", r.Method)
					w.WriteHeader(500)
				}
				if r.Method == http.MethodPut || r.Method == http.MethodPost {
					q := r.URL.Query()
					if q.Get("ttl") != "24h" || q.Get("exportable") != "false" {
						t.Errorf("creation query = %s, want ttl=24h and exportable=false", r.URL.RawQuery)
					}
				}
			}), true)
			args := []string{"upload", "--ttl", "24h", "--exportable=false", "--content-type", "text/test"}
			wantCalls := []string{"PUT"}
			if resumable {
				args = append(args, "--resumable")
				wantCalls = []string{"POST", "PATCH", "GET"}
			}
			var out, stderr bytes.Buffer
			if err := runDepot(c, append(args, path), &out, &stderr); err != nil || !json.Valid(out.Bytes()) {
				t.Fatalf("upload returned %v, stdout = %q, stderr = %q", err, &out, &stderr)
			}
			if got := depotCalls(calls); !slices.Equal(got, wantCalls) {
				t.Errorf("calls = %v, want %v", got, wantCalls)
			}
			if resumable && !strings.Contains(stderr.String(), "upload ID: test") {
				t.Errorf("stderr = %q, want resumable ID", &stderr)
			}
		})
	}
}

func depotCalls(calls chan string) []string {
	close(calls)
	var got []string
	for call := range calls {
		got = append(got, call)
	}
	return got
}

func TestDepotResume(t *testing.T) {
	path := depotTestFile(t, "abc")
	for _, tt := range []struct {
		name, length, offset, state, verdict, metadata string
		methods                                        []string
		wantError                                      string
	}{
		{"partial", "3", "1", "", "", "wagon-sha256 ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=", []string{"HEAD", "PATCH", "GET"}, ""},
		{"same size edited", "3", "1", "", "", "wagon-sha256 pS0VnyYrLG3bckphhAvvw26zDIiHekAwtly+himESck=", []string{"HEAD"}, "local file does not match the upload checksum"},
		{"legacy filename", "3", "1", "", "", "filename dGVzdCBmaWxlKy50eHQ=", []string{"HEAD", "PATCH", "GET"}, ""},
		{"legacy wrong filename", "3", "1", "", "", "filename b3RoZXIudHh0", []string{"HEAD"}, "local filename does not match the upload metadata"},
		{"legacy no filename", "3", "1", "", "", "", []string{"HEAD", "PATCH", "GET"}, ""},
		{"finalize", "3", "3", "pending_upload", "", "", []string{"HEAD", "GET", "PATCH", "GET"}, ""},
		{"completed", "3", "3", "active", "", "", []string{"HEAD", "GET"}, ""},
		{"infected", "3", "3", "quarantined", "infected", "", []string{"HEAD", "GET"}, "malware detected"},
		{"length mismatch", "4", "1", "", "", "", []string{"HEAD"}, "local file has 3 bytes"},
		{"invalid offset", "3", "4", "", "", "", []string{"HEAD"}, "invalid resumable upload offset"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := make(chan string, 5)
			var changed atomic.Bool
			c := depotTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls <- r.Method
				if r.URL.Path != "/api/v1/files/upload-1" {
					t.Errorf("path = %s, want /api/v1/files/upload-1", r.URL.Path)
				}
				switch r.Method {
				case http.MethodHead:
					if r.Header.Get("Tus-Resumable") != "1.0.0" {
						t.Errorf("HEAD has wrong Tus-Resumable header")
					}
					w.Header().Set("Upload-Length", tt.length)
					w.Header().Set("Upload-Offset", tt.offset)
					if tt.metadata != "" {
						w.Header().Set("Upload-Metadata", tt.metadata)
					}
					w.WriteHeader(http.StatusNoContent)
				case http.MethodPatch:
					body, err := io.ReadAll(r.Body)
					want := ""
					if tt.offset == "1" {
						want = "bc"
					}
					if r.Header.Get("Upload-Offset") != tt.offset || string(body) != want || err != nil {
						t.Errorf("PATCH offset = %s, body = %q, err = %v, want %s and %q", r.Header.Get("Upload-Offset"), body, err, tt.offset, want)
					}
					changed.Store(true)
					w.Header().Set("Upload-Offset", "3")
					w.WriteHeader(http.StatusNoContent)
				case http.MethodGet:
					state := tt.state
					if changed.Load() {
						state = "active"
					}
					jsonHandler(http.StatusOK, map[string]any{"id": "upload-1", "state": state, "verdict": tt.verdict})(w, r)
				default:
					t.Errorf("unexpected method %s", r.Method)
					w.WriteHeader(500)
				}
			}), true)
			var out, stderr bytes.Buffer
			err := runDepot(c, []string{"upload", "--resume", "upload-1", path}, &out, &stderr)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || out.Len() != 0 {
					t.Errorf("resume error = %v, stdout = %q, want %q and empty stdout", err, &out, tt.wantError)
				}
			} else if err != nil || !json.Valid(out.Bytes()) {
				t.Errorf("resume returned %v, stdout = %q, want metadata JSON", err, &out)
			}
			if !strings.Contains(stderr.String(), "upload ID: upload-1") {
				t.Errorf("stderr = %q, want resumable ID", &stderr)
			}
			if got := depotCalls(calls); !slices.Equal(got, tt.methods) {
				t.Errorf("requests = %v, want %v", got, tt.methods)
			}
		})
	}
}

func TestDepotUploadLocation(t *testing.T) {
	path := depotTestFile(t, "")
	for _, tt := range []struct {
		name, location string
		status         int
		wantID         bool
	}{
		{"scanner unavailable", "/api/v1/files/retry-1", 503, true},
		{"external location", "https://other.example/api/v1/files/retry-1", 201, false},
		{"traversal", "/api/v1/files/../retry-1", 201, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			c := depotTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost {
					t.Error("requested an invalid location")
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Location", tt.location)
				w.Header().Set("Upload-Offset", "0")
				w.WriteHeader(tt.status)
			}), true)
			var stderr bytes.Buffer
			err := runDepot(c, []string{"upload", "--resumable", path}, io.Discard, &stderr)
			if err == nil || calls.Load() != 1 || strings.Contains(stderr.String(), "upload ID: retry-1") != tt.wantID {
				t.Errorf("create error = %v, calls = %d, stderr = %q, want one failed request, show ID = %v", err, calls.Load(), &stderr, tt.wantID)
			}
		})
	}
}
