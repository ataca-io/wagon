package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shell installer targets Linux and macOS")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	installer, err := filepath.Abs("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		os        string
		arch      string
		target    string
		checksum  string
		failure   string
		missing   string
		existing  bool
		onPath    bool
		shadowed  bool
		wantError string
	}{
		{name: "Linux x64", os: "Linux", arch: "x86_64", target: "linux-amd64"},
		{name: "Linux amd64 alias", os: "Linux", arch: "amd64", target: "linux-amd64"},
		{name: "Linux ARM64", os: "Linux", arch: "aarch64", target: "linux-arm64"},
		{name: "Linux arm64 alias", os: "Linux", arch: "arm64", target: "linux-arm64"},
		{name: "macOS ARM64", os: "Darwin", arch: "arm64", target: "darwin-arm64"},
		{name: "sha256sum", os: "Linux", arch: "x86_64", target: "linux-amd64", checksum: "sha256sum"},
		{name: "shasum fallback", os: "Darwin", arch: "arm64", target: "darwin-arm64", checksum: "shasum"},
		{name: "upgrade", os: "Linux", arch: "x86_64", target: "linux-amd64", existing: true, onPath: true},
		{name: "shadowed installation", os: "Linux", arch: "x86_64", target: "linux-amd64", onPath: true, shadowed: true},
		{name: "macOS Intel", os: "Darwin", arch: "x86_64", wantError: "unsupported platform"},
		{name: "Windows", os: "MINGW64_NT", arch: "x86_64", wantError: "unsupported platform"},
		{name: "Linux 32-bit", os: "Linux", arch: "i686", wantError: "unsupported platform"},
		{name: "missing curl", os: "Linux", arch: "x86_64", missing: "curl", wantError: "required command not found: curl"},
		{name: "missing unzip", os: "Linux", arch: "x86_64", missing: "unzip", wantError: "required command not found: unzip"},
		{name: "missing checksum tool", os: "Linux", arch: "x86_64", missing: "checksum", wantError: "SHA-256 verification requires"},
		{name: "latest lookup failure", os: "Linux", arch: "x86_64", failure: "latest", wantError: "could not resolve the latest release"},
		{name: "unexpected redirect", os: "Linux", arch: "x86_64", failure: "redirect", wantError: "unexpected latest release URL"},
		{name: "archive download failure", os: "Linux", arch: "x86_64", failure: "archive", wantError: "could not download wagon-linux-amd64.zip"},
		{name: "manifest download failure", os: "Linux", arch: "x86_64", failure: "manifest", wantError: "could not download SHA256SUMS"},
		{name: "checksum mismatch", os: "Linux", arch: "x86_64", failure: "checksum", wantError: "checksum verification failed"},
		{name: "missing checksum", os: "Linux", arch: "x86_64", failure: "entry", wantError: "checksum missing"},
		{name: "invalid ZIP", os: "Linux", arch: "x86_64", failure: "zip", wantError: "could not extract wagon"},
		{name: "missing binary", os: "Linux", arch: "x86_64", failure: "binary", wantError: "could not extract wagon"},
		{name: "chmod failure", os: "Linux", arch: "x86_64", failure: "chmod", wantError: "could not make wagon executable"},
		{name: "replace failure", os: "Linux", arch: "x86_64", failure: "mv", wantError: "could not install wagon"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home with spaces")
			bin := filepath.Join(root, "tools")
			tmp := filepath.Join(root, "tmp")
			destination := filepath.Join(home, ".local", "bin", "wagon")
			for _, dir := range []string{bin, tmp, home} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, tool := range []string{"awk", "cat", "chmod", "cp", "mkdir", "mktemp", "mv", "rm", "unzip"} {
				if tool == tt.failure {
					writeInstallFile(t, filepath.Join(bin, tool), "#!/bin/sh\nexit 1\n", 0o755)
					continue
				}
				if tool != tt.missing {
					linkInstallTool(t, bin, tool)
				}
			}
			if tt.missing != "checksum" {
				tool := tt.checksum
				if tool == "" {
					tool = "sha256sum"
					if _, err := exec.LookPath(tool); err != nil {
						tool = "shasum"
					}
				}
				if _, err := exec.LookPath(tool); err != nil {
					t.Skipf("%s is not installed", tool)
				}
				linkInstallTool(t, bin, tool)
			}
			writeInstallFile(t, filepath.Join(bin, "uname"), `#!/bin/sh
case "$1" in
    -s) printf '%s\n' "$INSTALL_TEST_OS" ;;
    -m) printf '%s\n' "$INSTALL_TEST_ARCH" ;;
    *) exit 1 ;;
esac
`, 0o755)
			if tt.missing != "curl" {
				writeInstallFile(t, filepath.Join(bin, "curl"), installMockCurl, 0o755)
			}
			const binary = "#!/bin/sh\nprintf 'wagon v9.8.7\\n'\n"
			var archive bytes.Buffer
			zw := zip.NewWriter(&archive)
			entry := "wagon"
			if tt.failure == "binary" {
				entry = "other"
			}
			w, err := zw.Create(entry)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(binary)); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if tt.failure == "zip" {
				archive.Reset()
				archive.WriteString("not a ZIP")
			}
			writeInstallFile(t, filepath.Join(root, "archive.zip"), archive.String(), 0o600)
			digest := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
			if tt.failure == "checksum" {
				digest = strings.Repeat("0", 64)
			}
			var manifest strings.Builder
			for _, target := range []string{"linux-amd64", "linux-arm64", "darwin-arm64", "windows-amd64"} {
				if tt.failure != "entry" || target != "linux-amd64" {
					fmt.Fprintf(&manifest, "%s  wagon-%s.zip\n", digest, target)
				}
			}
			writeInstallFile(t, filepath.Join(root, "SHA256SUMS"), manifest.String(), 0o600)
			if tt.existing || tt.wantError != "" {
				if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
					t.Fatal(err)
				}
				writeInstallFile(t, destination, "previous binary", 0o755)
			}
			if tt.shadowed {
				writeInstallFile(t, filepath.Join(bin, "wagon"), "previous binary", 0o755)
			}
			path := bin
			if tt.onPath {
				path += string(os.PathListSeparator) + filepath.Dir(destination)
			}
			cmd := exec.CommandContext(t.Context(), shell, installer)
			cmd.Env = []string{
				"HOME=" + home, "PATH=" + path, "TMPDIR=" + tmp,
				"INSTALL_TEST_OS=" + tt.os, "INSTALL_TEST_ARCH=" + tt.arch,
				"INSTALL_TEST_ROOT=" + root, "INSTALL_TEST_FAILURE=" + tt.failure,
			}
			out, err := cmd.CombinedOutput()
			if tt.wantError != "" {
				if err == nil || !strings.Contains(string(out), tt.wantError) {
					t.Fatalf("error = %v, output = %s; want %q", err, out, tt.wantError)
				}
				assertInstallFile(t, destination, "previous binary")
			} else {
				if err != nil {
					t.Fatalf("installer: %v\n%s", err, out)
				}
				assertInstallFile(t, destination, binary)
				info, err := os.Stat(destination)
				if err != nil || info.Mode().Perm() != 0o755 {
					t.Fatalf("installed permissions: info = %v, error = %v; want 0755", info, err)
				}
				wantHint := !tt.onPath || tt.shadowed
				if strings.Contains(string(out), "export PATH=") != wantHint {
					t.Errorf("output = %s; want PATH hint = %t", out, wantHint)
				}
				if !strings.Contains(string(out), "Installed wagon v9.8.7 to "+destination) {
					t.Errorf("missing installation confirmation: %s", out)
				}
				assertInstallFile(t, filepath.Join(root, "requests"), "https://github.com/ataca-io/wagon/releases/latest\n"+
					"https://github.com/ataca-io/wagon/releases/download/v9.8.7/wagon-"+tt.target+".zip\n"+
					"https://github.com/ataca-io/wagon/releases/download/v9.8.7/SHA256SUMS\n")
			}
			for _, dir := range []string{tmp, filepath.Dir(destination)} {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() != "wagon" {
						t.Errorf("temporary file left behind: %s", filepath.Join(dir, entry.Name()))
					}
				}
			}
		})
	}
}

func linkInstallTool(t *testing.T, bin, name string) {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
		t.Fatal(err)
	}
}

func writeInstallFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func assertInstallFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Errorf("%s = %q, error = %v; want %q", path, got, err, want)
	}
}

const installMockCurl = `#!/bin/sh
for url do :; done
printf '%s\n' "$url" >> "$INSTALL_TEST_ROOT/requests"
output=
while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then output=$2; shift; fi
    shift
done
case "$url" in
    https://github.com/ataca-io/wagon/releases/latest)
        [ "$INSTALL_TEST_FAILURE" != latest ] || exit 22
        if [ "$INSTALL_TEST_FAILURE" = redirect ]; then
            printf 'https://github.com/login'
        else
            printf 'https://github.com/ataca-io/wagon/releases/tag/v9.8.7'
        fi
        ;;
    https://github.com/ataca-io/wagon/releases/download/v9.8.7/wagon-*.zip)
        [ "$INSTALL_TEST_FAILURE" != archive ] || exit 22
        cp "$INSTALL_TEST_ROOT/archive.zip" "$output"
        ;;
    https://github.com/ataca-io/wagon/releases/download/v9.8.7/SHA256SUMS)
        [ "$INSTALL_TEST_FAILURE" != manifest ] || exit 22
        cp "$INSTALL_TEST_ROOT/SHA256SUMS" "$output"
        ;;
    *) printf 'unexpected URL: %s\n' "$url" >&2; exit 1 ;;
esac
`
