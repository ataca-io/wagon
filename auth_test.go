package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAuthSetup(t *testing.T) {
	pki := mintClientCert(t)
	other := mintClientCert(t)

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "installs a pair", args: []string{"setup", pki.certFile, pki.keyFile}},
		{name: "one path", args: []string{"setup", pki.certFile}, wantErr: true},
		{name: "no sub-verb", args: []string{pki.certFile, pki.keyFile}, wantErr: true},
		{name: "unknown sub-verb", args: []string{"bogus"}, wantErr: true},
		{name: "missing file", args: []string{"setup", filepath.Join(t.TempDir(), "gone.crt"), pki.keyFile}, wantErr: true},
		{name: "mismatched pair", args: []string{"setup", pki.certFile, other.keyFile}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			var stdout bytes.Buffer
			err := runAuth(tt.args, &stdout)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("runAuth(%v) = nil, want error", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("runAuth(%v) = %v, want nil", tt.args, err)
			}

			cert, key, err := resolveKeys("", "")
			if err != nil {
				t.Fatalf("resolveKeys after setup = %v, want nil", err)
			}
			if want := filepath.Join(home, ".wagon", authCert); cert != want {
				t.Errorf("cert = %s, want %s", cert, want)
			}
			if want := filepath.Join(home, ".wagon", authKey); key != want {
				t.Errorf("key = %s, want %s", key, want)
			}

			// A copy, not a link: the bytes must match and the source must
			// still stand on its own.
			got, err := os.ReadFile(cert)
			if err != nil {
				t.Fatalf("ReadFile(%s) = %v", cert, err)
			}
			want, err := os.ReadFile(pki.certFile)
			if err != nil {
				t.Fatalf("ReadFile(%s) = %v", pki.certFile, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("installed cert differs from %s", pki.certFile)
			}
		})
	}
}

// A second setup must replace the first, not fail on the existing file.
func TestRunAuthSetupReplaces(t *testing.T) {
	first, second := mintClientCert(t), mintClientCert(t)
	t.Setenv("HOME", t.TempDir())

	var stdout bytes.Buffer
	if err := runAuth([]string{"setup", first.certFile, first.keyFile}, &stdout); err != nil {
		t.Fatalf("first setup = %v, want nil", err)
	}
	if err := runAuth([]string{"setup", second.certFile, second.keyFile}, &stdout); err != nil {
		t.Fatalf("second setup = %v, want nil", err)
	}

	cert, _, err := resolveKeys("", "")
	if err != nil {
		t.Fatalf("resolveKeys = %v, want nil", err)
	}
	got, err := os.ReadFile(cert)
	if err != nil {
		t.Fatalf("ReadFile = %v", err)
	}
	want, err := os.ReadFile(second.certFile)
	if err != nil {
		t.Fatalf("ReadFile = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("installed cert is not the second one")
	}
}

// Both halves are secrets: neither may be group- or world-readable.
func TestRunAuthSetupPermissions(t *testing.T) {
	pki := mintClientCert(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runAuth([]string{"setup", pki.certFile, pki.keyFile}, &bytes.Buffer{}); err != nil {
		t.Fatalf("setup = %v, want nil", err)
	}

	tests := []struct {
		path string
		want os.FileMode
	}{
		{filepath.Join(home, ".wagon"), 0o700},
		{filepath.Join(home, ".wagon", authCert), 0o600},
		{filepath.Join(home, ".wagon", authKey), 0o600},
	}
	for _, tt := range tests {
		info, err := os.Stat(tt.path)
		if err != nil {
			t.Fatalf("Stat(%s) = %v", tt.path, err)
		}
		if got := info.Mode().Perm(); got != tt.want {
			t.Errorf("%s mode = %o, want %o", tt.path, got, tt.want)
		}
	}
}

func TestRunAuthShow(t *testing.T) {
	pki := mintClientCert(t)
	t.Setenv("HOME", t.TempDir())

	var empty bytes.Buffer
	if err := runAuth(nil, &empty); err != nil {
		t.Fatalf("runAuth(nil) = %v, want nil", err)
	}
	if !strings.Contains(empty.String(), "no certificate") {
		t.Errorf("unconfigured output = %q, want it to say no certificate", empty.String())
	}

	if err := runAuth([]string{"setup", pki.certFile, pki.keyFile}, &bytes.Buffer{}); err != nil {
		t.Fatalf("setup = %v, want nil", err)
	}
	var shown bytes.Buffer
	if err := runAuth([]string{"show"}, &shown); err != nil {
		t.Fatalf("runAuth(show) = %v, want nil", err)
	}
	for _, want := range []string{"client", "expires", "senders"} {
		if !strings.Contains(shown.String(), want) {
			t.Errorf("show output = %q, want it to include %q", shown.String(), want)
		}
	}
}

// The installed pair must carry a real request, with no --cert or --key flag.
func TestRunUsesInstalledKeys(t *testing.T) {
	pki := mintClientCert(t)
	url, caFile := newTLSServer(t, jsonHandler(http.StatusOK, map[string]any{"client_id": "testclient"}), pki.clientCAs)

	t.Setenv("HOME", t.TempDir())
	t.Setenv("RAIL_CERT", "")
	t.Setenv("RAIL_KEY", "")
	t.Setenv("RAIL_CA", caFile)

	if err := runAuth([]string{"setup", pki.certFile, pki.keyFile}, &bytes.Buffer{}); err != nil {
		t.Fatalf("setup = %v, want nil", err)
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"--server", url, "whoami"}, &stdout, &stderr); got != 0 {
		t.Fatalf("run exit = %d, want 0; stderr = %s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "testclient") {
		t.Errorf("stdout = %q, want it to name testclient", stdout.String())
	}
}
