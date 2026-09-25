package main

import (
	"cmp"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Installed certificate names inside wagonDir. wagon falls back to these when
// neither the flags nor the environment name a certificate.
const (
	authCert = "client.crt"
	authKey  = "client.key"
)

// wagonDir is ~/.wagon, or a relative .wagon if the home directory cannot be
// resolved. Callers always stat the result, so a wrong guess surfaces as a
// missing file rather than a panic.
func wagonDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".wagon"
	}
	return filepath.Join(home, ".wagon")
}

// fileExists reports whether path can be stat'ed. A certificate that is simply
// not installed yet is a state to report, not an error.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolveKeys fills in the certificate pair from ~/.wagon when neither the
// flag nor the environment names one, and reports a missing file with the verb
// that fixes it.
func resolveKeys(certFile, keyFile string) (string, string, error) {
	dir := wagonDir()
	cert := cmp.Or(certFile, filepath.Join(dir, authCert))
	key := cmp.Or(keyFile, filepath.Join(dir, authKey))
	for _, p := range []string{cert, key} {
		if !fileExists(p) {
			return "", "", fmt.Errorf("no client certificate at %s: run `wagon auth setup <cert> <key>`, or pass --cert and --key", p)
		}
	}
	return cert, key, nil
}

// runAuth handles `wagon auth [setup <cert> <key>]`. With no arguments it
// reports the installed certificate.
func runAuth(args []string, stdout io.Writer) error {
	switch {
	case len(args) == 0, len(args) == 1 && args[0] == "show":
		return showAuth(stdout)
	case args[0] == "setup" && len(args) == 3:
		return setupAuth(args[1], args[2], stdout)
	default:
		return &usageError{"usage: wagon auth setup <cert> <key>"}
	}
}

// setupAuth copies a certificate pair into ~/.wagon, so later commands need no
// flags. It parses the pair from the bytes it is about to write, so a
// mismatched cert and key fails here rather than at the next TLS handshake.
func setupAuth(certFile, keyFile string, stdout io.Writer) error {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return fmt.Errorf("error reading %s: %w", certFile, err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("error reading %s: %w", keyFile, err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("error loading the pair: %w", err)
	}

	dir := wagonDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("error creating %s: %w", dir, err)
	}
	if err := installSecret(dir, authCert, certPEM, stdout); err != nil {
		return err
	}
	if err := installSecret(dir, authKey, keyPEM, stdout); err != nil {
		return err
	}
	return printCert(stdout, filepath.Join(dir, authCert), pair)
}

// installSecret writes pem to dir/name at mode 0600, replacing any existing
// file. Both halves of the pair are secrets, so neither is ever group- or
// world-readable.
func installSecret(dir, name string, pem []byte, stdout io.Writer) error {
	dst := filepath.Join(dir, name)
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error replacing %s: %w", dst, err)
	}
	if err := os.WriteFile(dst, pem, 0o600); err != nil {
		return fmt.Errorf("error writing %s: %w", dst, err)
	}
	_, _ = fmt.Fprintf(stdout, "installed %s\n", dst)
	return nil
}

// showAuth reports the installed certificate, or says there is none. A missing
// pair is a state to print, not an error.
func showAuth(stdout io.Writer) error {
	dir := wagonDir()
	cert, key := filepath.Join(dir, authCert), filepath.Join(dir, authKey)
	if !fileExists(cert) {
		_, _ = fmt.Fprintf(stdout, "no certificate in %s; run `wagon auth setup <cert> <key>`\n", dir)
		return nil
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return fmt.Errorf("error loading %s: %w", cert, err)
	}
	return printCert(stdout, cert, pair)
}

// printCert renders who a certificate identifies and when it expires, read
// from the certificate itself rather than from the server.
func printCert(stdout io.Writer, path string, pair tls.Certificate) error {
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("error parsing %s: %w", path, err)
	}
	renderKV(stdout, [][2]string{
		{"certificate", path},
		{"client", leaf.Subject.CommonName},
		{"senders", orDash(strings.Join(leaf.EmailAddresses, ", "))},
		{"expires", fmt.Sprintf("%s (%d days)",
			leaf.NotAfter.UTC().Format(time.RFC3339), int(time.Until(leaf.NotAfter).Hours()/24))},
	})
	return nil
}
