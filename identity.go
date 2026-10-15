package main

import (
	"bytes"
	"cmp"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

// Identity storage. The identity named defaultIdentity lives in wagonDir
// itself, so installs that predate named identities keep working.
const (
	defaultIdentity = "default"
	identitiesDir   = "identities"
	configFile      = "config.json"
)

// identityName is lowercase only, because macOS file systems ignore case. It
// allows no slash and no leading dot, so a name cannot leave identitiesDir.
var identityName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// validIdentity reports a usage error when name is not a legal identity name.
func validIdentity(name string) error {
	if !identityName.MatchString(name) {
		return &usageError{fmt.Sprintf("invalid identity name %q: use 1 to 64 characters from a-z, 0-9, '.', '_', '-', starting with a letter or digit", name)}
	}
	return nil
}

// identityDir is the directory that holds the named identity's certificate pair.
func identityDir(name string) string {
	if name == defaultIdentity {
		return wagonDir()
	}
	return filepath.Join(wagonDir(), identitiesDir, name)
}

// config is ~/.wagon/config.json. An empty Identity means defaultIdentity.
type config struct {
	Identity string `json:"identity"`
}

// loadConfig reads config.json. A missing file is the zero config, but a
// malformed one is an error, so a typo never selects another identity.
func loadConfig() (config, error) {
	var cfg config
	path := filepath.Join(wagonDir(), configFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("error reading %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return config{}, fmt.Errorf("error parsing %s: %w", path, err)
	}
	return cfg, nil
}

// saveConfig writes config.json at mode 0600 under a 0700 wagonDir.
func saveConfig(cfg config) error {
	dir := wagonDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("error creating %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("error encoding config: %w", err)
	}
	path := filepath.Join(dir, configFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("error replacing %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("error writing %s: %w", path, err)
	}
	return nil
}

// activeIdentity picks the override, else the config's identity, else
// defaultIdentity. An override skips config.json; a malformed config.json is
// an error otherwise, never a silent fallback.
func activeIdentity(override string) (string, error) {
	if override != "" {
		return override, validIdentity(override)
	}
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	name := cmp.Or(cfg.Identity, defaultIdentity)
	if err := validIdentity(name); err != nil {
		return "", err
	}
	return name, nil
}

// installed reports whether the named identity has both client.crt and
// client.key. A symlinked directory counts.
func installed(name string) bool {
	dir := identityDir(name)
	return fileExists(filepath.Join(dir, authCert)) && fileExists(filepath.Join(dir, authKey))
}

// identityNames lists the installed identities in sorted order. An identity
// is installed when it has both client.crt and client.key.
func identityNames() ([]string, error) {
	var names []string
	if installed(defaultIdentity) {
		names = append(names, defaultIdentity)
	}
	entries, err := os.ReadDir(filepath.Join(wagonDir(), identitiesDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("error reading identities: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == defaultIdentity || validIdentity(name) != nil {
			continue
		}
		if installed(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, nil
}

// loadLeaf parses the first certificate in a PEM file. It reads no key.
func loadLeaf(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading %s: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", path, err)
	}
	return leaf, nil
}
