package main

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"
)

func runAuthRenew(server, certFile, keyFile, caFile, identity string, jsonOut bool, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("auth renew", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outputDir := fs.String("output-dir", "", "new directory for the renewed certificate and key (required)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}
	if *outputDir == "" || fs.NArg() != 0 {
		return &usageError{"usage: auth renew --output-dir NEW_DIR"}
	}

	certFile, keyFile, err := resolveKeys(certFile, keyFile, identity)
	if err != nil {
		return &usageError{err.Error()}
	}
	c, err := newClient(server, certFile, keyFile, caFile, jsonOut)
	if err != nil {
		return &usageError{err.Error()}
	}
	return renewCertificate(c, *outputDir, out)
}

func renewCertificate(c *client, outputDir string, out io.Writer) error {
	transport := c.http.Transport.(*http.Transport)
	defer transport.CloseIdleConnections()
	c.http.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("certificate renewal requires a direct connection")
	}

	pair := transport.TLSClientConfig.Certificates[0]
	leaf := pair.Leaf
	path := "/api/v1/certs/renew"
	var request struct {
		CSRPEM  string `json:"csr_pem"`
		CertPEM string `json:"cert_pem,omitempty"`
	}
	var key crypto.Signer
	if time.Now().After(leaf.NotAfter) {
		// The expired path uses the same key as proof and sends no TLS certificate.
		signer, ok := pair.PrivateKey.(crypto.Signer)
		if !ok {
			return errors.New("client key cannot sign a certificate request")
		}
		key = signer
		path = "/certs/renew"
		request.CertPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))
		transport.TLSClientConfig.Certificates = nil
	} else {
		_, signer, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Errorf("generate renewal key: %w", err)
		}
		key = signer
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: leaf.Subject}, key)
	if err != nil {
		return fmt.Errorf("create renewal CSR: %w", err)
	}
	request.CSRPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode renewal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := os.Mkdir(outputDir, 0o700); err != nil {
		return &usageError{fmt.Sprintf("create renewal output directory: %v", err)}
	}
	dir, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("open renewal output directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	// Keep the new key if the server issues the certificate but the response is lost.
	if err := writeRenewalFile(dir, authKey, keyPEM); err != nil {
		return fmt.Errorf("stage renewal key: %w", err)
	}

	var response struct {
		CertPEM        string `json:"cert_pem"`
		PreviousSerial string `json:"previous_serial"`
		ActiveCerts    int    `json:"active_certs"`
	}
	raw, err := c.do(http.MethodPost, path, request, &response)
	if err != nil {
		return fmt.Errorf("renew certificate (key staged in %q): %w", outputDir, err)
	}

	certPEM := []byte(response.CertPEM)
	renewed, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("validate renewed certificate (key staged in %q): %w", outputDir, err)
	}
	cert := renewed.Leaf
	if err := validateRenewedCertificate(leaf, cert, time.Now()); err != nil {
		return fmt.Errorf("validate renewed certificate (key staged in %q): %w", outputDir, err)
	}
	if err := writeRenewalFile(dir, authCert, certPEM); err != nil {
		return fmt.Errorf("stage renewed certificate (key staged in %q): %w", outputDir, err)
	}

	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderKV(out, [][2]string{
		{"certificate", filepath.Join(outputDir, authCert)},
		{"key", filepath.Join(outputDir, authKey)},
		{"serial", fmt.Sprintf("%x", cert.SerialNumber)},
		{"expires", expiry(cert)},
		{"previous_serial", response.PreviousSerial},
		{"active_certs", fmt.Sprint(response.ActiveCerts)},
	})
	return nil
}

func validateRenewedCertificate(old, renewed *x509.Certificate, now time.Time) error {
	switch {
	case renewed.IsCA || renewed.Subject.CommonName != old.Subject.CommonName:
		return errors.New("unexpected certificate identity")
	case now.Before(renewed.NotBefore) || now.After(renewed.NotAfter):
		return errors.New("certificate is outside its validity period")
	case !slices.Contains(renewed.ExtKeyUsage, x509.ExtKeyUsageClientAuth):
		return errors.New("certificate does not permit client authentication")
	case !slices.Equal(old.EmailAddresses, renewed.EmailAddresses):
		return errors.New("certificate email SANs changed")
	case !renewedWebhookSANsMatch(old.URIs, renewed.URIs):
		return errors.New("certificate webhook URI SANs changed")
	}
	return nil
}

func renewedWebhookSANsMatch(old, renewed []*url.URL) bool {
	for _, u := range old {
		// Rail copies the original HTTPS webhook URIs in order.
		if u.Scheme != "https" {
			continue
		}
		if len(renewed) == 0 || renewed[0].String() != u.String() {
			return false
		}
		renewed = renewed[1:]
	}
	return len(renewed) == 0
}

func writeRenewalFile(dir *os.Root, name string, data []byte) error {
	f, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return errors.Join(err, f.Close())
	}
	return errors.Join(f.Sync(), f.Close())
}
