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

// resolveKeys fills in the certificate pair from the active identity when the
// flags and the environment do not name one, and reports a missing file with
// the verb that fixes it. It reads the config only when it needs the identity.
func resolveKeys(certFile, keyFile, identity string) (string, string, error) {
	cert, key := certFile, keyFile
	fix := "`wagon auth setup <cert> <key>`"
	if cert == "" || key == "" {
		name, err := activeIdentity(identity)
		if err != nil {
			return "", "", err
		}
		if name != defaultIdentity {
			fix = fmt.Sprintf("`wagon auth add %s <cert> <key>`", name)
		}
		dir := identityDir(name)
		cert = cmp.Or(certFile, filepath.Join(dir, authCert))
		key = cmp.Or(keyFile, filepath.Join(dir, authKey))
	}
	for _, p := range []string{cert, key} {
		if !fileExists(p) {
			return "", "", fmt.Errorf("no client certificate at %s: run %s, or pass --cert and --key", p, fix)
		}
	}
	return cert, key, nil
}

// runAuth handles `wagon auth`. With no arguments it reports the active
// identity's certificate. identity is the --identity flag or its environment.
func runAuth(args []string, identity string, stdout io.Writer) error {
	switch {
	case len(args) == 0, len(args) == 1 && args[0] == "show":
		return showAuth(identity, stdout)
	case args[0] == "setup" && len(args) == 3:
		return addIdentity(defaultIdentity, args[1], args[2], stdout)
	case args[0] == "add" && len(args) == 4:
		return addIdentity(args[1], args[2], args[3], stdout)
	case args[0] == "use" && len(args) == 2:
		return useIdentity(args[1], identity, stdout)
	case args[0] == "list" && len(args) == 1:
		return listIdentities(identity, stdout)
	default:
		return &usageError{"usage: wagon auth <show|setup|add|use|list> [args]"}
	}
}

// addIdentity copies a certificate pair into the named identity's directory.
// It parses the pair from the bytes it is about to write, so a mismatch fails
// here, not at the next TLS handshake. It never changes the active identity.
func addIdentity(name, certFile, keyFile string, stdout io.Writer) error {
	if err := validIdentity(name); err != nil {
		return err
	}
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

	dir := identityDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("error creating %s: %w", dir, err)
	}
	if err := installSecret(dir, authCert, certPEM, stdout); err != nil {
		return err
	}
	if err := installSecret(dir, authKey, keyPEM, stdout); err != nil {
		return err
	}
	if err := printCert(stdout, name, filepath.Join(dir, authCert), pair); err != nil {
		return err
	}
	if active, _ := activeIdentity(""); active != name {
		_, _ = fmt.Fprintf(stdout, "run `wagon auth use %s` to make it active\n", name)
	}
	return nil
}

// useIdentity makes an installed identity the active one in config.json. It
// notes an override from --identity or WAGON_IDENTITY, which still wins.
func useIdentity(name, override string, stdout io.Writer) error {
	if err := validIdentity(name); err != nil {
		return err
	}
	dir := identityDir(name)
	if !installed(name) {
		return fmt.Errorf("no identity %q in %s: run `wagon auth add %s <cert> <key>`", name, dir, name)
	}
	if err := saveConfig(config{Identity: name}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "active identity: %s\n", name)
	if override != "" && override != name {
		_, _ = fmt.Fprintf(stdout, "note: --identity or WAGON_IDENTITY overrides this with %s\n", override)
	}
	return nil
}

// listIdentities prints one row per installed identity and marks the active one.
func listIdentities(identity string, stdout io.Writer) error {
	active, err := activeIdentity(identity)
	if err != nil {
		return err
	}
	names, err := identityNames()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		_, _ = fmt.Fprintln(stdout, "no identities; run `wagon auth add <name> <cert> <key>`")
		return nil
	}
	rows := make([][]string, 0, len(names))
	for _, name := range names {
		mark, client, expires := "", "-", "-"
		if name == active {
			mark = "*"
		}
		if leaf, err := loadLeaf(filepath.Join(identityDir(name), authCert)); err == nil {
			client, expires = orDash(leaf.Subject.CommonName), expiry(leaf)
		}
		rows = append(rows, []string{mark, name, client, expires})
	}
	renderTable(stdout, []string{"ACTIVE", "NAME", "CLIENT", "EXPIRES"}, rows)
	return nil
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

// showAuth reports the active identity's certificate, or says there is none.
// A missing pair is a state to print, not an error.
func showAuth(identity string, stdout io.Writer) error {
	name, err := activeIdentity(identity)
	if err != nil {
		return err
	}
	dir := identityDir(name)
	cert, key := filepath.Join(dir, authCert), filepath.Join(dir, authKey)
	if !fileExists(cert) {
		if name == defaultIdentity {
			_, _ = fmt.Fprintf(stdout, "no certificate in %s; run `wagon auth setup <cert> <key>`\n", dir)
			return nil
		}
		_, _ = fmt.Fprintf(stdout, "no certificate for identity %s in %s; run `wagon auth add %s <cert> <key>`\n", name, dir, name)
		return nil
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return fmt.Errorf("error loading %s: %w", cert, err)
	}
	return printCert(stdout, name, cert, pair)
}

// printCert renders who a certificate identifies and when it expires, read
// from the certificate itself rather than from the server.
func printCert(stdout io.Writer, identity, path string, pair tls.Certificate) error {
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("error parsing %s: %w", path, err)
	}
	renderKV(stdout, [][2]string{
		{"identity", identity},
		{"certificate", path},
		{"client", leaf.Subject.CommonName},
		{"senders", orDash(strings.Join(leaf.EmailAddresses, ", "))},
		{"expires", expiry(leaf)},
	})
	return nil
}

// expiry renders a certificate's NotAfter with the days left.
func expiry(leaf *x509.Certificate) string {
	return fmt.Sprintf("%s (%d days)",
		leaf.NotAfter.UTC().Format(time.RFC3339), int(time.Until(leaf.NotAfter).Hours()/24))
}
