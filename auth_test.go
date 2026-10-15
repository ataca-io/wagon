package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
			err := runAuth(tt.args, "", &stdout)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("runAuth(%v) = nil, want error", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("runAuth(%v) = %v, want nil", tt.args, err)
			}

			cert, key, err := resolveKeys("", "", "")
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
	if err := runAuth([]string{"setup", first.certFile, first.keyFile}, "", &stdout); err != nil {
		t.Fatalf("first setup = %v, want nil", err)
	}
	if err := runAuth([]string{"setup", second.certFile, second.keyFile}, "", &stdout); err != nil {
		t.Fatalf("second setup = %v, want nil", err)
	}

	cert, _, err := resolveKeys("", "", "")
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

	if err := runAuth([]string{"setup", pki.certFile, pki.keyFile}, "", &bytes.Buffer{}); err != nil {
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
	if err := runAuth(nil, "", &empty); err != nil {
		t.Fatalf("runAuth(nil) = %v, want nil", err)
	}
	if !strings.Contains(empty.String(), "no certificate") {
		t.Errorf("unconfigured output = %q, want it to say no certificate", empty.String())
	}

	if err := runAuth([]string{"setup", pki.certFile, pki.keyFile}, "", &bytes.Buffer{}); err != nil {
		t.Fatalf("setup = %v, want nil", err)
	}
	var shown bytes.Buffer
	if err := runAuth([]string{"show"}, "", &shown); err != nil {
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
	t.Setenv("WAGON_IDENTITY", "")
	t.Setenv("RAIL_CA", caFile)

	if err := runAuth([]string{"setup", pki.certFile, pki.keyFile}, "", &bytes.Buffer{}); err != nil {
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

func TestIdentityNameValidation(t *testing.T) {
	pki := mintClientCert(t)
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"work", false},
		{"prod.eu", false},
		{"a-1_b", false},
		{"", true},
		{"Work", true},
		{"../x", true},
		{"a/b", true},
		{".hidden", true},
		{strings.Repeat("a", 65), true},
	}
	for _, tt := range tests {
		t.Run(strconv.Quote(tt.name), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if err := runAuth([]string{"add", tt.name, pki.certFile, pki.keyFile}, "", io.Discard); (err != nil) != tt.wantErr {
				t.Errorf("add %q error = %v, want error %v", tt.name, err, tt.wantErr)
			}
			if tt.wantErr {
				if _, ok := errors.AsType[*usageError](runAuth([]string{"use", tt.name}, "", io.Discard)); !ok {
					t.Errorf("use %q error is not a usage error", tt.name)
				}
			}
		})
	}
}

func TestResolveKeysIdentities(t *testing.T) {
	root, work := mintClientCert(t), mintClientCert(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WAGON_IDENTITY", "")
	for _, args := range [][]string{
		{"setup", root.certFile, root.keyFile},
		{"add", "work", work.certFile, work.keyFile},
	} {
		if err := runAuth(args, "", io.Discard); err != nil {
			t.Fatalf("runAuth(%v) = %v", args, err)
		}
	}
	wagon := filepath.Join(home, ".wagon")
	workDir := filepath.Join(wagon, "identities", "work")

	// add must not switch identity.
	if cert, _, err := resolveKeys("", "", ""); err != nil || cert != filepath.Join(wagon, authCert) {
		t.Fatalf("resolveKeys after add = %q, %v, want the default pair", cert, err)
	}
	if err := runAuth([]string{"use", "work"}, "", io.Discard); err != nil {
		t.Fatalf("use work = %v", err)
	}

	tests := []struct {
		name, cert, key, identity string
		wantCert, wantKey         string
	}{
		{"config picks work", "", "", "", filepath.Join(workDir, authCert), filepath.Join(workDir, authKey)},
		{"identity default", "", "", "default", filepath.Join(wagon, authCert), filepath.Join(wagon, authKey)},
		{"explicit pair wins", root.certFile, root.keyFile, "work", root.certFile, root.keyFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cert, key, err := resolveKeys(tt.cert, tt.key, tt.identity)
			if err != nil {
				t.Fatalf("resolveKeys(%q, %q, %q) = %v", tt.cert, tt.key, tt.identity, err)
			}
			if cert != tt.wantCert || key != tt.wantKey {
				t.Errorf("resolveKeys(%q, %q, %q) = %q, %q, want %q, %q", tt.cert, tt.key, tt.identity, cert, key, tt.wantCert, tt.wantKey)
			}
		})
	}

	// Modes.
	for path, want := range map[string]os.FileMode{
		filepath.Join(wagon, "identities"): 0o700,
		workDir:                            0o700,
		filepath.Join(workDir, authCert):   0o600,
		filepath.Join(workDir, authKey):    0o600,
		filepath.Join(wagon, configFile):   0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s) = %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}

	// A malformed config breaks only calls that need it.
	if err := os.WriteFile(filepath.Join(wagon, configFile), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveKeys("", "", ""); err == nil {
		t.Error("resolveKeys with malformed config = nil, want error")
	}
	if _, _, err := resolveKeys(root.certFile, root.keyFile, ""); err != nil {
		t.Errorf("resolveKeys with explicit pair and malformed config = %v, want nil", err)
	}
}

func TestUseMissingIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runAuth([]string{"use", "ghost"}, "", io.Discard); err == nil {
		t.Fatal("use ghost = nil, want error")
	}
	if _, err := os.Stat(filepath.Join(home, ".wagon", configFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("config.json Stat error = %v, want not exist", err)
	}
}

func TestRunAuthList(t *testing.T) {
	root, work := mintClientCert(t), mintClientCert(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WAGON_IDENTITY", "")

	var empty bytes.Buffer
	if err := runAuth([]string{"list"}, "", &empty); err != nil {
		t.Fatalf("list on empty HOME = %v, want nil", err)
	}
	if !strings.Contains(empty.String(), "wagon auth add") {
		t.Errorf("empty list output = %q, want the add hint", empty.String())
	}

	for _, args := range [][]string{
		{"setup", root.certFile, root.keyFile},
		{"add", "work", work.certFile, work.keyFile},
		{"use", "work"},
	} {
		if err := runAuth(args, "", io.Discard); err != nil {
			t.Fatalf("runAuth(%v) = %v", args, err)
		}
	}

	tests := []struct {
		identity, marked, unmarked string
	}{
		{"", "work", "default"},
		{"default", "default", "work"},
	}
	for _, tt := range tests {
		t.Run("identity "+tt.identity, func(t *testing.T) {
			var out bytes.Buffer
			if err := runAuth([]string{"list"}, tt.identity, &out); err != nil {
				t.Fatalf("list = %v", err)
			}
			for line := range strings.SplitSeq(out.String(), "\n") {
				switch {
				case strings.Contains(line, tt.marked) && !strings.HasPrefix(line, "*"):
					t.Errorf("line %q is not marked active", line)
				case strings.Contains(line, tt.unmarked) && strings.HasPrefix(line, "*"):
					t.Errorf("line %q is marked active", line)
				}
			}
			for _, want := range []string{"default", "work"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("list output = %q, want it to include %q", out.String(), want)
				}
			}
		})
	}
}

func TestRunAuthShowIdentity(t *testing.T) {
	work := mintClientCert(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WAGON_IDENTITY", "")
	if err := runAuth([]string{"add", "work", work.certFile, work.keyFile}, "", io.Discard); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runAuth([]string{"show"}, "work", &out); err != nil {
		t.Fatalf("show = %v", err)
	}
	if !strings.Contains(out.String(), "identity") || !strings.Contains(out.String(), "work") {
		t.Errorf("show output = %q, want an identity row naming work", out.String())
	}
}

// The identity flag, the environment, and the config pick different pairs.
func TestRunSelectsIdentity(t *testing.T) {
	work, other := mintClientCert(t), mintClientCert(t)
	url, caFile := newTLSServer(t, jsonHandler(http.StatusOK, map[string]any{"client_id": "testclient"}), work.clientCAs)

	t.Setenv("HOME", t.TempDir())
	t.Setenv("RAIL_CERT", "")
	t.Setenv("RAIL_KEY", "")
	t.Setenv("WAGON_IDENTITY", "")
	t.Setenv("RAIL_CA", caFile)
	for _, args := range [][]string{
		{"setup", other.certFile, other.keyFile},
		{"add", "work", work.certFile, work.keyFile},
	} {
		if err := runAuth(args, "", io.Discard); err != nil {
			t.Fatalf("runAuth(%v) = %v", args, err)
		}
	}

	exit := func(args ...string) int {
		return run(append([]string{"--server", url}, append(args, "whoami")...), io.Discard, io.Discard)
	}
	if got := exit(); got != 2 {
		t.Errorf("default identity exit = %d, want 2", got)
	}
	if got := exit("--identity", "work"); got != 0 {
		t.Errorf("--identity work exit = %d, want 0", got)
	}
	if err := runAuth([]string{"use", "work"}, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := exit(); got != 0 {
		t.Errorf("config work exit = %d, want 0", got)
	}
	if got := exit("--identity", "default"); got != 2 {
		t.Errorf("--identity default exit = %d, want 2", got)
	}
	t.Setenv("WAGON_IDENTITY", "default")
	if got := run([]string{"--server", url, "whoami"}, io.Discard, io.Discard); got != 2 {
		t.Errorf("WAGON_IDENTITY=default exit = %d, want 2", got)
	}
}

// setupHome installs a default pair and a "work" identity under a fresh HOME.
func setupHome(t *testing.T) (home string, root, work testPKI) {
	t.Helper()
	root, work = mintClientCert(t), mintClientCert(t)
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WAGON_IDENTITY", "")
	for _, args := range [][]string{
		{"setup", root.certFile, root.keyFile},
		{"add", "work", work.certFile, work.keyFile},
	} {
		if err := runAuth(args, "", io.Discard); err != nil {
			t.Fatalf("runAuth(%v) = %v", args, err)
		}
	}
	return home, root, work
}

func TestEmptyConfigIsDefault(t *testing.T) {
	for _, content := range []string{"", " \n\t"} {
		t.Run(strconv.Quote(content), func(t *testing.T) {
			home, _, _ := setupHome(t)
			if err := os.WriteFile(filepath.Join(home, ".wagon", configFile), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			cert, _, err := resolveKeys("", "", "")
			if want := filepath.Join(home, ".wagon", authCert); err != nil || cert != want {
				t.Errorf("resolveKeys = %q, %v, want %q", cert, err, want)
			}
		})
	}
}

func TestOverrideSkipsMalformedConfig(t *testing.T) {
	home, _, _ := setupHome(t)
	if err := os.WriteFile(filepath.Join(home, ".wagon", configFile), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveKeys("", "", "work"); err != nil {
		t.Errorf("resolveKeys with override = %v, want nil", err)
	}
	if err := runAuth([]string{"list"}, "work", io.Discard); err != nil {
		t.Errorf("list with override = %v, want nil", err)
	}
}

func TestUseTightensConfigMode(t *testing.T) {
	home, _, _ := setupHome(t)
	path := filepath.Join(home, ".wagon", configFile)
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runAuth([]string{"use", "work"}, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("config.json mode = %o, want 600", got)
	}
}

func TestAddHint(t *testing.T) {
	_, root, work := setupHome(t)
	hint := "wagon auth use work"
	var out bytes.Buffer
	if err := runAuth([]string{"add", "work", work.certFile, work.keyFile}, "", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), hint) {
		t.Errorf("add with default active = %q, want the hint %q", out.String(), hint)
	}

	if err := runAuth([]string{"use", "work"}, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runAuth([]string{"add", "work", work.certFile, work.keyFile}, "", &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), hint) {
		t.Errorf("add with work active = %q, want no hint", out.String())
	}

	// setup replaces default and leaves work active.
	if err := runAuth([]string{"setup", root.certFile, root.keyFile}, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	cert, _, err := resolveKeys("", "", "")
	if want := identityDir("work"); err != nil || filepath.Dir(cert) != want {
		t.Errorf("resolveKeys after setup = %q, %v, want a pair in %s", cert, err, want)
	}
}

func TestInstalledMeansBothFiles(t *testing.T) {
	home, _, work := setupHome(t)
	ids := filepath.Join(home, ".wagon", identitiesDir)
	partial := filepath.Join(ids, "partial")
	if err := os.MkdirAll(partial, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(work.certFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, authCert), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ids, "work"), filepath.Join(ids, "linked")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runAuth([]string{"list"}, "", &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "partial") {
		t.Errorf("list = %q, want no partial identity", out.String())
	}
	if !strings.Contains(out.String(), "linked") {
		t.Errorf("list = %q, want the symlinked identity", out.String())
	}
	if err := runAuth([]string{"use", "partial"}, "", io.Discard); err == nil {
		t.Error("use partial = nil, want error")
	}
}

func TestDefaultPathMessages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WAGON_IDENTITY", "")
	if _, _, err := resolveKeys("", "", ""); err == nil || !strings.Contains(err.Error(), "wagon auth setup") {
		t.Errorf("resolveKeys error = %v, want it to name wagon auth setup", err)
	}
	var out bytes.Buffer
	if err := runAuth(nil, "", &out); err != nil || !strings.Contains(out.String(), "no certificate in") {
		t.Errorf("show = %q, %v, want %q", out.String(), err, "no certificate in")
	}
	if _, _, err := resolveKeys("", "", "work"); err == nil || !strings.Contains(err.Error(), "wagon auth add work") {
		t.Errorf("resolveKeys error = %v, want it to name wagon auth add work", err)
	}
}

func TestUseNotesOverride(t *testing.T) {
	setupHome(t)
	var out bytes.Buffer
	if err := runAuth([]string{"use", "work"}, "default", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "note: ") {
		t.Errorf("use output = %q, want the override note", out.String())
	}
}
