package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ataca-io/wagon/internal/buildinfo"
)

// clientTimeout bounds every request; wagon talks to one server per
// invocation, so there is no reason for a call to hang.
const clientTimeout = 30 * time.Second

// client is the mTLS HTTP client wagon uses to speak to rail's API.
type client struct {
	base    string
	http    *http.Client
	jsonOut bool
	// caPool trusts --ca's roots for a server certificate; nil means system
	// roots. Shared with the depot upload client (attach.go), which verifies
	// depot's server certificate but sends no client certificate of its own.
	caPool *x509.CertPool
}

// newClient loads the client certificate and optional CA, and builds the
// mTLS client. A load failure here is a bad flag value, not a network
// problem, so callers should treat it as a usage error.
func newClient(server, certFile, keyFile, caFile string, jsonOut bool) (*client, error) {
	u, perr := url.Parse(server)
	if perr != nil || u.Scheme != "https" || u.Host == "" {
		return nil, &usageError{fmt.Sprintf("--server must be an https URL (got %q)", server)}
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load client certificate: %w", err)
	}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	var caPool *x509.CertPool
	if caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		caPool = x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("parse CA file: no certificates found")
		}
		tlsCfg.RootCAs = caPool
	}
	return &client{
		base:    strings.TrimSuffix(server, "/"),
		http:    &http.Client{Timeout: clientTimeout, Transport: &http.Transport{TLSClientConfig: tlsCfg}},
		jsonOut: jsonOut,
		caPool:  caPool,
	}, nil
}

// errorResponse is the wire shape of every API error body: {"error","message"}.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// apiError is an HTTP-level failure (status >= 400) from the server, carrying
// the decoded error code and message when the body parsed as errorResponse.
type apiError struct {
	status  int
	code    string
	message string
}

func newAPIError(status int, body []byte) *apiError {
	e := &apiError{status: status}
	var er errorResponse
	if json.Unmarshal(body, &er) == nil {
		e.code = er.Error
		e.message = er.Message
	}
	return e
}

func (e *apiError) Error() string {
	if e.code != "" || e.message != "" {
		return fmt.Sprintf("%s: %s", e.code, e.message)
	}
	return fmt.Sprintf("%d %s", e.status, http.StatusText(e.status))
}

// authError signals an mTLS/TLS handshake failure or an auth-shaped HTTP
// status (401/403) — exit code 2.
type authError struct{ err error }

func (e *authError) Error() string { return e.err.Error() }
func (e *authError) Unwrap() error { return e.err }

// netError signals a transport failure (connection refused, timeout, DNS) —
// exit code 4.
type netError struct{ err error }

func (e *netError) Error() string { return e.err.Error() }
func (e *netError) Unwrap() error { return e.err }

// usageError signals a bad flag, missing argument, or unknown verb — exit
// code 1.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// classifyTransportErr sorts a failure from http.Client.Do into an auth
// failure (a TLS or certificate problem, however deeply net/http wrapped it)
// or a plain transport failure (refused, timed out, DNS).
func classifyTransportErr(err error) error {
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return &authError{err}
	}
	// A server-side rejection ("remote error: tls: bad certificate") arrives
	// as crypto/tls's unexported alert type, so only its text identifies it.
	msg := err.Error()
	if strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") {
		return &authError{err}
	}
	return &netError{err}
}

// exitCode maps an error returned from run into the process exit code.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if _, ok := errors.AsType[*usageError](err); ok {
		return 1
	}
	if _, ok := errors.AsType[*authError](err); ok {
		return 2
	}
	if api, ok := errors.AsType[*apiError](err); ok {
		switch {
		case api.status == http.StatusUnauthorized || api.status == http.StatusForbidden:
			return 2
		case api.status >= 400 && api.status < 500:
			return 3
		default:
			return 4
		}
	}
	// netError, and anything else unclassified, is a transport-shaped
	// failure by default.
	return 4
}

// do sends a JSON request and, on success, decodes the JSON response body
// into out (when non-nil) and also returns the raw body, for --json to print
// verbatim. A status >= 400 is reported as *apiError, never decoded into out.
func (c *client) do(method, path string, body, out any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "wagon/"+buildinfo.Version())
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classifyTransportErr(err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, newAPIError(resp.StatusCode, data)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
	}
	return data, nil
}
