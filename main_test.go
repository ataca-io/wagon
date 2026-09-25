package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testPKI is a client certificate minted for one test, plus the CA pool a
// test server should trust it against.
type testPKI struct {
	certFile, keyFile string
	clientCAs         *x509.CertPool
}

func mintClientCert(t *testing.T) testPKI {
	t.Helper()
	dir := t.TempDir()

	caKey := newKey(t)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caKey.Public(), caKey)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}

	key := newKey(t)
	leafTmpl := &x509.Certificate{
		SerialNumber:   big.NewInt(2),
		Subject:        pkix.Name{CommonName: "testclient"},
		EmailAddresses: []string{"agent@example.com"},
		NotBefore:      time.Now().Add(-time.Hour),
		NotAfter:       time.Now().Add(24 * time.Hour),
		KeyUsage:       x509.KeyUsageDigitalSignature,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, key.Public(), caKey)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
	}

	certFile := filepath.Join(dir, "client.crt")
	writePEM(t, certFile, "CERTIFICATE", leafDER)
	keyFile := filepath.Join(dir, "client.key")
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	writePEM(t, keyFile, "PRIVATE KEY", der)

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return testPKI{certFile: certFile, keyFile: keyFile, clientCAs: pool}
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newTLSServer starts an httptest server requiring mTLS against clientCAs,
// and returns its URL plus a --ca file wagon should trust (the server's own
// self-signed leaf, exactly as httptest mints it).
func newTLSServer(t *testing.T, handler http.Handler, clientCAs *x509.CertPool) (serverURL, caFile string) {
	t.Helper()
	ts := httptest.NewUnstartedServer(handler)
	ts.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientCAs,
	}
	ts.StartTLS()
	t.Cleanup(ts.Close)

	caFile = filepath.Join(t.TempDir(), "server-ca.crt")
	writePEM(t, caFile, "CERTIFICATE", ts.Certificate().Raw)
	return ts.URL, caFile
}

func jsonHandler(status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
}

// whoHandler mirrors WhoAPI (internal/httpd/who.go): GET returns JSON only
// when Accept asks for it, otherwise the HTML inspection page.
func whoHandler(status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html></html>"))
			return
		}
		jsonHandler(status, body)(w, r)
	}
}

func baseArgs(serverURL string, pki testPKI, caFile string, verb ...string) []string {
	args := []string{"--server", serverURL, "--cert", pki.certFile, "--key", pki.keyFile, "--ca", caFile}
	return append(args, verb...)
}

func TestRunWhoami(t *testing.T) {
	pki := mintClientCert(t)
	who := map[string]any{
		"client_id":         "testclient",
		"valid":             true,
		"key_type":          "Ed25519",
		"serial":            "abc123",
		"not_before":        "2024-01-01T00:00:00Z",
		"not_after":         "2025-01-01T00:00:00Z",
		"expires_in_days":   10,
		"renew_recommended": false,
		"senders":           []string{"agent@example.com"},
		"webhooks":          []string{},
	}
	url, caFile := newTLSServer(t, whoHandler(http.StatusOK, who), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	got := run(baseArgs(url, pki, caFile, "whoami"), &stdout, &stderr)
	if got != 0 {
		t.Fatalf("run(whoami) exit = %d, want 0; stderr = %s", got, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("testclient")) {
		t.Errorf("run(whoami) stdout = %q, want it to contain the client id", stdout.String())
	}
}

func TestRunWhoamiJSON(t *testing.T) {
	pki := mintClientCert(t)
	who := map[string]any{"client_id": "testclient"}
	url, caFile := newTLSServer(t, whoHandler(http.StatusOK, who), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	args := baseArgs(url, pki, caFile, "whoami")
	args = append([]string{"--json"}, args...)
	got := run(args, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("run(--json whoami) exit = %d, want 0; stderr = %s", got, stderr.String())
	}
	want := "\"client_id\": \"testclient\""
	if !bytes.Contains(stdout.Bytes(), []byte(want)) {
		t.Errorf("run(--json whoami) stdout = %q, want it to contain %q", stdout.String(), want)
	}
}

func TestRunSendBuildsRequestBody(t *testing.T) {
	pki := mintClientCert(t)

	var captured map[string]any
	handler := func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		jsonHandler(http.StatusAccepted, map[string]any{
			"id": "01msg", "request_id": "req-1", "recipients": 2, "status": "queued",
		})(w, r)
	}
	url, caFile := newTLSServer(t, http.HandlerFunc(handler), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	args := baseArgs(url, pki, caFile, "send",
		"--from", "a@x.com", "--to", "b@x.com", "--to", "c@x.com",
		"--subject", "hi", "--text", "hello", "--request-id", "req-1")
	got := run(args, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("run(send) exit = %d, want 0; stderr = %s", got, stderr.String())
	}

	want := map[string]any{
		"from":       "a@x.com",
		"to":         []any{"b@x.com", "c@x.com"},
		"subject":    "hi",
		"body_text":  "hello",
		"request_id": "req-1",
	}
	for k, v := range want {
		got, ok := captured[k]
		if !ok {
			t.Fatalf("request body missing field %q; got %v", k, captured)
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(v)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("request body field %q = %s, want %s", k, gotJSON, wantJSON)
		}
	}
	if !bytes.Contains(stdout.Bytes(), []byte("01msg")) {
		t.Errorf("run(send) stdout = %q, want it to contain the message id", stdout.String())
	}
}

func TestRunExitCodes(t *testing.T) {
	pki := mintClientCert(t)

	tests := []struct {
		name     string
		setup    func(t *testing.T) (serverURL, caFile string)
		verb     []string
		wantExit int
	}{
		{
			name: "handshake failure: server trusts no client CA",
			setup: func(t *testing.T) (string, string) {
				return newTLSServer(t, jsonHandler(http.StatusOK, nil), x509.NewCertPool())
			},
			verb:     []string{"whoami"},
			wantExit: 2,
		},
		{
			name: "401 unauthorized",
			setup: func(t *testing.T) (string, string) {
				return newTLSServer(t, jsonHandler(http.StatusUnauthorized, map[string]string{"error": "unauthorized", "message": "client certificate required"}), pki.clientCAs)
			},
			verb:     []string{"whoami"},
			wantExit: 2,
		},
		{
			name: "403 forbidden",
			setup: func(t *testing.T) (string, string) {
				return newTLSServer(t, jsonHandler(http.StatusForbidden, map[string]string{"error": "cert_invalid", "message": "certificate validation failed"}), pki.clientCAs)
			},
			verb:     []string{"whoami"},
			wantExit: 2,
		},
		{
			name: "404 not found",
			setup: func(t *testing.T) (string, string) {
				return newTLSServer(t, jsonHandler(http.StatusNotFound, map[string]string{"error": "not_found", "message": "message not found"}), pki.clientCAs)
			},
			verb:     []string{"messages", "deliveries", "01xyz"},
			wantExit: 3,
		},
		{
			name: "500 internal error",
			setup: func(t *testing.T) (string, string) {
				return newTLSServer(t, jsonHandler(http.StatusInternalServerError, map[string]string{"error": "internal_error", "message": "boom"}), pki.clientCAs)
			},
			verb:     []string{"whoami"},
			wantExit: 4,
		},
		{
			name: "connection refused",
			setup: func(t *testing.T) (string, string) {
				ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("listen: %v", err)
				}
				addr := ln.Addr().String()
				_ = ln.Close()
				return "https://" + addr, ""
			},
			verb:     []string{"whoami"},
			wantExit: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, caFile := tt.setup(t)
			var stdout, stderr bytes.Buffer
			args := baseArgs(url, pki, caFile, tt.verb...)
			got := run(args, &stdout, &stderr)
			if got != tt.wantExit {
				t.Errorf("run(%v) exit = %d, want %d; stderr = %s", args, got, tt.wantExit, stderr.String())
			}
		})
	}
}

func TestRunUsageErrors(t *testing.T) {
	pki := mintClientCert(t)

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "no certificate anywhere",
			args: []string{"--server", "https://127.0.0.1:1", "whoami"},
		},
		{
			name: "unknown verb",
			args: []string{"--server", "https://127.0.0.1:1", "--cert", pki.certFile, "--key", pki.keyFile, "bogus"},
		},
		{
			name: "no verb at all",
			args: []string{"--server", "https://127.0.0.1:1", "--cert", pki.certFile, "--key", pki.keyFile},
		},
		{
			name: "non-https --server",
			args: []string{"--server", "http://127.0.0.1:1", "--cert", pki.certFile, "--key", pki.keyFile, "whoami"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// An empty HOME keeps the ~/.wagon fallback out of these cases.
			t.Setenv("HOME", t.TempDir())
			t.Setenv("RAIL_SERVER", "")
			t.Setenv("RAIL_CERT", "")
			t.Setenv("RAIL_KEY", "")
			t.Setenv("RAIL_CA", "")

			var stdout, stderr bytes.Buffer
			got := run(tt.args, &stdout, &stderr)
			if got != 1 {
				t.Errorf("run(%v) exit = %d, want 1; stderr = %s", tt.args, got, stderr.String())
			}
		})
	}
}

func TestRunEnvFallbackAndFlagPrecedence(t *testing.T) {
	pki := mintClientCert(t)
	url, caFile := newTLSServer(t, jsonHandler(http.StatusOK, map[string]any{"client_id": "testclient"}), pki.clientCAs)

	t.Run("env fallback", func(t *testing.T) {
		t.Setenv("RAIL_SERVER", url)
		t.Setenv("RAIL_CERT", pki.certFile)
		t.Setenv("RAIL_KEY", pki.keyFile)
		t.Setenv("RAIL_CA", caFile)

		var stdout, stderr bytes.Buffer
		got := run([]string{"whoami"}, &stdout, &stderr)
		if got != 0 {
			t.Fatalf("run(whoami) with env fallback exit = %d, want 0; stderr = %s", got, stderr.String())
		}
	})

	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv("RAIL_SERVER", "https://bogus.invalid:1")
		t.Setenv("RAIL_CERT", pki.certFile)
		t.Setenv("RAIL_KEY", pki.keyFile)
		t.Setenv("RAIL_CA", caFile)

		var stdout, stderr bytes.Buffer
		got := run([]string{"--server", url, "whoami"}, &stdout, &stderr)
		if got != 0 {
			t.Fatalf("run(--server whoami) exit = %d, want 0; stderr = %s", got, stderr.String())
		}
	})
}

// captureBodyHandler decodes each request's JSON body into captured and
// replies with a fixed response.
func captureBodyHandler(captured *map[string]any, status int, resp any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(captured)
		jsonHandler(status, resp)(w, r)
	}
}

func TestFormsUpdateBody(t *testing.T) {
	pki := mintClientCert(t)

	tests := []struct {
		name     string
		args     []string
		wantExit int
		wantBody map[string]any
	}{
		{
			name:     "space-separated boolean is a usage error, not a silent enable",
			args:     []string{"forms", "update", "tok1", "--enabled", "false"},
			wantExit: 1,
		},
		{
			name:     "no flags sends an empty patch",
			args:     []string{"forms", "update", "tok1"},
			wantExit: 0,
			wantBody: map[string]any{},
		},
		{
			name:     "--upload-max-files 0 sends max_files 0",
			args:     []string{"forms", "update", "tok1", "--upload-max-files", "0"},
			wantExit: 0,
			wantBody: map[string]any{"uploads": map[string]any{"max_files": float64(0)}},
		},
		{
			name:     "--enabled=false sends enabled false",
			args:     []string{"forms", "update", "tok1", "--enabled=false"},
			wantExit: 0,
			wantBody: map[string]any{"enabled": false},
		},
		{
			name:     "--upload-ttl alone is a usage error",
			args:     []string{"forms", "update", "tok1", "--upload-ttl", "24h"},
			wantExit: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured map[string]any
			url, caFile := newTLSServer(t, captureBodyHandler(&captured, http.StatusOK, map[string]any{"token": "tok1"}), pki.clientCAs)

			var stdout, stderr bytes.Buffer
			args := baseArgs(url, pki, caFile, tt.args...)
			got := run(args, &stdout, &stderr)
			if got != tt.wantExit {
				t.Fatalf("run(%v) exit = %d, want %d; stderr = %s", args, got, tt.wantExit, stderr.String())
			}
			if tt.wantExit != 0 {
				return
			}
			gotJSON, _ := json.Marshal(captured)
			wantJSON, _ := json.Marshal(tt.wantBody)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("request body = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestFormsCreateBody(t *testing.T) {
	pki := mintClientCert(t)

	var captured map[string]any
	url, caFile := newTLSServer(t, captureBodyHandler(&captured, http.StatusCreated, map[string]any{"token": "tok1"}), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	args := baseArgs(url, pki, caFile, "forms", "create",
		"--from", "agent@example.com", "--name", "contact", "--subject", "hi",
		"--to", "a@x.com", "--to", "b@x.com")
	got := run(args, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("run(forms create) exit = %d, want 0; stderr = %s", got, stderr.String())
	}

	want := map[string]any{
		"name":    "contact",
		"from":    "agent@example.com",
		"to":      []any{"a@x.com", "b@x.com"},
		"subject": "hi",
	}
	for k, v := range want {
		gv, ok := captured[k]
		if !ok {
			t.Fatalf("request body missing field %q; got %v", k, captured)
		}
		gotJSON, _ := json.Marshal(gv)
		wantJSON, _ := json.Marshal(v)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("request body field %q = %s, want %s", k, gotJSON, wantJSON)
		}
	}
	if _, ok := captured["uploads"]; ok {
		t.Errorf("request body has an unexpected uploads field: %v", captured["uploads"])
	}
}

func TestRunTestBuildsRequestBody(t *testing.T) {
	pki := mintClientCert(t)

	var captured map[string]any
	resp := map[string]any{"id": "01msg", "from": "a@x.com", "to": "b@example.com", "status": "queued"}
	url, caFile := newTLSServer(t, captureBodyHandler(&captured, http.StatusOK, resp), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	args := baseArgs(url, pki, caFile, "test", "--to", "b@example.com")
	got := run(args, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("run(test) exit = %d, want 0; stderr = %s", got, stderr.String())
	}

	want := map[string]any{"to": "b@example.com"}
	gotJSON, _ := json.Marshal(captured)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("request body = %s, want %s", gotJSON, wantJSON)
	}
}

func TestSendBodyFile(t *testing.T) {
	pki := mintClientCert(t)

	t.Run("--body-file - reads stdin into body_text", func(t *testing.T) {
		var captured map[string]any
		resp := map[string]any{"id": "01msg", "request_id": "r1", "recipients": 1, "status": "queued"}
		url, caFile := newTLSServer(t, captureBodyHandler(&captured, http.StatusAccepted, resp), pki.clientCAs)

		tmp := writeAttachFixture(t, "stdin", "hello from stdin")
		f, err := os.Open(tmp)
		if err != nil {
			t.Fatalf("open stdin fixture: %v", err)
		}
		t.Cleanup(func() { _ = f.Close() })
		oldStdin := os.Stdin
		os.Stdin = f
		t.Cleanup(func() { os.Stdin = oldStdin })

		var stdout, stderr bytes.Buffer
		args := baseArgs(url, pki, caFile, "send", "--from", "a@x.com", "--to", "b@x.com",
			"--subject", "hi", "--body-file", "-")
		got := run(args, &stdout, &stderr)
		if got != 0 {
			t.Fatalf("run(send --body-file -) exit = %d, want 0; stderr = %s", got, stderr.String())
		}
		if captured["body_text"] != "hello from stdin" {
			t.Errorf("request body_text = %v, want %q", captured["body_text"], "hello from stdin")
		}
	})

	t.Run("--text and --body-file are mutually exclusive", func(t *testing.T) {
		url, caFile := newTLSServer(t, jsonHandler(http.StatusOK, nil), pki.clientCAs)
		var stdout, stderr bytes.Buffer
		args := baseArgs(url, pki, caFile, "send", "--from", "a@x.com", "--to", "b@x.com",
			"--subject", "hi", "--text", "x", "--body-file", "f")
		got := run(args, &stdout, &stderr)
		if got != 1 {
			t.Errorf("run(send --text --body-file) exit = %d, want 1; stderr = %s", got, stderr.String())
		}
	})
}

func TestRunSendHelpAndBadFlag(t *testing.T) {
	pki := mintClientCert(t)

	t.Run("-h exits 0 and prints the flag table", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		args := []string{"--server", "https://127.0.0.1:1", "--cert", pki.certFile, "--key", pki.keyFile, "send", "-h"}
		got := run(args, &stdout, &stderr)
		if got != 0 {
			t.Fatalf("run(send -h) exit = %d, want 0; stderr = %s", got, stderr.String())
		}
		if !strings.Contains(stderr.String(), "-subject") {
			t.Errorf("run(send -h) stderr = %q, want it to contain the flag table", stderr.String())
		}
	})

	t.Run("--bogus exits 1 and prints the flag table", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		args := []string{"--server", "https://127.0.0.1:1", "--cert", pki.certFile, "--key", pki.keyFile, "send", "--bogus"}
		got := run(args, &stdout, &stderr)
		if got != 1 {
			t.Fatalf("run(send --bogus) exit = %d, want 1; stderr = %s", got, stderr.String())
		}
		if !strings.Contains(stderr.String(), "-subject") {
			t.Errorf("run(send --bogus) stderr = %q, want it to contain the flag table", stderr.String())
		}
	})
}

func TestClassifyTransportErr(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantExit int
	}{
		{
			name:     "wrapped *tls.CertificateVerificationError is an auth failure",
			err:      fmt.Errorf("dial: %w", &tls.CertificateVerificationError{}),
			wantExit: 2,
		},
		{
			name:     "tls text without a typed error hits the substring fallback",
			err:      fmt.Errorf("dial: %w", tls.AlertError(42)),
			wantExit: 2,
		},
		{
			name:     "*net.OpError is a transport failure",
			err:      &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			wantExit: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exitCode(classifyTransportErr(tt.err))
			if got != tt.wantExit {
				t.Errorf("exitCode(classifyTransportErr(%v)) = %d, want %d", tt.err, got, tt.wantExit)
			}
		})
	}
}
