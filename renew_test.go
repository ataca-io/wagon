package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthRenew(t *testing.T) {
	tests := []struct {
		name      string
		expired   bool
		jsonOut   bool
		loseReply bool
		redirect  bool
		wrongKey  bool
		wantErr   string
	}{
		{name: "rotate valid certificate"},
		{name: "renew expired certificate", expired: true, jsonOut: true},
		{name: "response lost after request", loseReply: true, wantErr: "key staged"},
		{name: "refuse redirect", redirect: true, wantErr: "requires a direct connection"},
		{name: "reject certificate for another key", wrongKey: true, wantErr: "validate renewed certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			expires := now.Add(time.Hour)
			if tt.expired {
				expires = now.Add(-time.Hour)
			}
			pki := newRenewalPKI(t, now, expires)
			outputDir := filepath.Join(t.TempDir(), "renewed")
			certPath, keyPath := filepath.Join(outputDir, authCert), filepath.Join(outputDir, authKey)
			var requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				path, peerCount := "/api/v1/certs/renew", 1
				if tt.expired {
					path, peerCount = "/certs/renew", 0
				}
				if r.Method != http.MethodPost || r.URL.Path != path || len(r.TLS.PeerCertificates) != peerCount {
					t.Errorf("request = %s %s with %d TLS certificates, want POST %s with %d", r.Method, r.URL.Path, len(r.TLS.PeerCertificates), path, peerCount)
					http.Error(w, "wrong renewal route", http.StatusBadRequest)
					return
				}
				if peerCount > 0 && !r.TLS.PeerCertificates[0].Equal(pki.leaf) {
					t.Error("mTLS did not present the old certificate")
				}
				if _, err := os.Stat(keyPath); err != nil {
					t.Errorf("staged key is missing when request arrives: %v", err)
				}
				var req struct {
					CSRPEM  string `json:"csr_pem"`
					CertPEM string `json:"cert_pem"`
				}
				dec := json.NewDecoder(r.Body)
				dec.DisallowUnknownFields()
				if err := dec.Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "malformed request", http.StatusBadRequest)
					return
				}
				if tt.expired {
					block, _ := pem.Decode([]byte(req.CertPEM))
					if block == nil || block.Type != "CERTIFICATE" || !bytes.Equal(block.Bytes, pki.leaf.Raw) {
						t.Error("request did not include the old public certificate")
					}
				} else if req.CertPEM != "" {
					t.Error("mTLS renewal included a certificate in the body")
				}
				block, _ := pem.Decode([]byte(req.CSRPEM))
				if block == nil || block.Type != "CERTIFICATE REQUEST" {
					t.Error("request did not include a PEM CSR")
					http.Error(w, "missing CSR", http.StatusBadRequest)
					return
				}
				csr, err := x509.ParseCertificateRequest(block.Bytes)
				if err != nil {
					t.Errorf("parse CSR: %v", err)
					http.Error(w, "malformed CSR", http.StatusBadRequest)
					return
				}
				if err := csr.CheckSignature(); err != nil {
					t.Errorf("CSR signature: %v", err)
				}
				if sameKey := bytes.Equal(csr.RawSubjectPublicKeyInfo, pki.leaf.RawSubjectPublicKeyInfo); sameKey != tt.expired {
					t.Errorf("CSR uses old key = %t, want %t", sameKey, tt.expired)
				}
				if tt.loseReply {
					panic(http.ErrAbortHandler)
				}

				if tt.redirect {
					http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
					return
				}

				publicKey := csr.PublicKey
				if tt.wrongKey {
					publicKey = pki.leaf.PublicKey
				}
				cert := &x509.Certificate{
					SerialNumber:   big.NewInt(3),
					Subject:        pki.leaf.Subject,
					NotBefore:      now.Add(-time.Minute),
					NotAfter:       now.Add(48 * time.Hour),
					EmailAddresses: pki.leaf.EmailAddresses,
					KeyUsage:       x509.KeyUsageDigitalSignature,
					ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
				}
				der, err := x509.CreateCertificate(rand.Reader, cert, pki.ca, publicKey, pki.caKey)
				if err != nil {
					t.Errorf("issue fixture certificate: %v", err)
					http.Error(w, "issue failed", http.StatusInternalServerError)
					return
				}
				jsonHandler(http.StatusOK, map[string]any{
					"cert_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
					"serial":   "3", "previous_serial": "2", "active_certs": 2,
				})(w, r)
			}))
			server.TLS = &tls.Config{ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pki.clientCAs}
			server.StartTLS()
			t.Cleanup(server.Close)
			caFile := filepath.Join(t.TempDir(), "server.crt")
			writePEM(t, caFile, "CERTIFICATE", server.Certificate().Raw)

			var out bytes.Buffer
			err := runAuthRenew(server.URL, pki.certFile, pki.keyFile, caFile, "", tt.jsonOut,
				[]string{"--output-dir", outputDir}, &out, io.Discard)
			if requests.Load() != 1 {
				t.Errorf("renewal requests = %d, want 1", requests.Load())
			}
			original, originalErr := tls.LoadX509KeyPair(pki.certFile, pki.keyFile)
			if originalErr != nil || !original.Leaf.Equal(pki.leaf) {
				t.Errorf("original pair changed: %v", originalErr)
			}
			checkRenewalMode(t, outputDir, 0o700)
			checkRenewalMode(t, keyPath, 0o600)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("renew returned %v, want error containing %q", err, tt.wantErr)
				}
				if _, err := os.Stat(certPath); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("stat certificate after failure = %v, want no certificate", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("renew returned %v, want success", err)
			}

			checkRenewalMode(t, certPath, 0o600)
			pair, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err != nil {
				t.Fatalf("load staged pair: %v", err)
			}
			if serial := pair.Leaf.SerialNumber.Int64(); serial != 3 {
				t.Errorf("staged serial = %d, want 3", serial)
			}
			if tt.jsonOut {
				var response struct {
					Serial string `json:"serial"`
				}
				if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Serial != "3" {
					t.Errorf("JSON serial = %q, error = %v, want 3", response.Serial, err)
				}
			} else if !strings.Contains(out.String(), certPath) || !strings.Contains(out.String(), keyPath) {
				t.Errorf("renewal output omits staged paths: %q", out.String())
			}
		})
	}
}

func TestAuthRenewRequiresNewOutputDirectory(t *testing.T) {
	pki := mintClientCert(t)
	outputDir := t.TempDir()
	err := runAuthRenew("https://127.0.0.1:1", pki.certFile, pki.keyFile, "", "", false,
		[]string{"--output-dir", outputDir}, io.Discard, io.Discard)
	if _, usage := errors.AsType[*usageError](err); !usage || !strings.Contains(err.Error(), "create renewal output directory") {
		t.Errorf("renew into existing directory = %v, want usage error about the directory", err)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil || len(entries) != 0 {
		t.Errorf("existing output directory changed: %d files, error = %v", len(entries), err)
	}
}

type renewalPKI struct {
	testPKI
	ca, leaf *x509.Certificate
	caKey    *ecdsa.PrivateKey
}

func newRenewalPKI(t *testing.T, now, expires time.Time) renewalPKI {
	t.Helper()
	caKey := newKey(t)
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(72 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	ca = createRenewalTestCertificate(t, ca, ca, caKey, caKey)
	leaf := &x509.Certificate{
		SerialNumber:   big.NewInt(2),
		Subject:        pkix.Name{CommonName: "testclient"},
		EmailAddresses: []string{"agent@example.com"},
		NotBefore:      now.Add(-2 * time.Hour),
		NotAfter:       expires,
		KeyUsage:       x509.KeyUsageDigitalSignature,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	key := newKey(t)
	leaf = createRenewalTestCertificate(t, leaf, ca, key, caKey)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, authCert), filepath.Join(dir, authKey)
	writePEM(t, certFile, "CERTIFICATE", leaf.Raw)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal fixture key: %v", err)
	}
	writePEM(t, keyFile, "PRIVATE KEY", keyDER)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return renewalPKI{testPKI{certFile: certFile, keyFile: keyFile, clientCAs: pool}, ca, leaf, caKey}
}

func createRenewalTestCertificate(t *testing.T, template, parent *x509.Certificate, key, signer *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, template, parent, key.Public(), signer)
	if err != nil {
		t.Fatalf("create fixture certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse fixture certificate: %v", err)
	}
	return cert
}

func checkRenewalMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if mode := info.Mode().Perm(); runtime.GOOS != "windows" && mode != want {
		t.Errorf("%s permissions = %o, want %o", path, mode, want)
	}
}
