package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// attachUploadTimeout bounds one file upload to depot; a send can carry whole
// files, so this is far longer than the mTLS client's own timeout.
const attachUploadTimeout = 5 * time.Minute

// attachFileTTL bounds how long an uploaded file lives in depot: a short TTL
// caps how long a file is stranded in rail's depot quota if the send that
// follows then fails.
const attachFileTTL = "1h"

// attachGrantRequest is the body wagon sends to mint a grant. content_types
// and origins are omitted: empty content_types means "any" at depot, and
// rail defaults origins itself.
type attachGrantRequest struct {
	MaxBytes int64  `json:"max_bytes"`
	MaxFiles int    `json:"max_files"`
	FileTTL  string `json:"file_ttl"`
}

// resolveAttachFiles mints one depot upload grant covering every path in
// paths, uploads each in flag order, and returns the resulting file ids in
// that order. It mints nothing and returns (nil, nil) when paths is empty.
func resolveAttachFiles(c *client, paths []string, out io.Writer) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	var maxSize int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, &usageError{fmt.Sprintf("--attach-file %s: %v", p, err)}
		}
		if !info.Mode().IsRegular() {
			return nil, &usageError{fmt.Sprintf("--attach-file %s: not a regular file", p)}
		}
		maxSize = max(maxSize, info.Size())
	}

	// depot's grant-upload check is a strict ">" against max_bytes, so a file
	// at exactly the cap already fits; depot separately refuses max_bytes<=0,
	// which max(maxSize, 1) guards against for an all-empty (zero-byte) input.
	var grant struct {
		UploadURL string `json:"upload_url"`
	}
	if _, err := c.do(http.MethodPost, "/api/v1/upload-grants",
		attachGrantRequest{MaxBytes: max(maxSize, 1), MaxFiles: len(paths), FileTTL: attachFileTTL}, &grant); err != nil {
		return nil, err
	}

	// No client certificate: the grant token in the URL is depot's
	// credential. --ca still applies, so a privately-issued depot server
	// certificate verifies the same way it does for rail's own mTLS calls.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: c.caPool}
	uploadClient := &http.Client{Timeout: attachUploadTimeout, Transport: transport}

	// Sequential by choice: depot's per-upload use-counting is an atomic
	// guarded UPDATE, so concurrency would be safe; serial keeps progress output ordered.
	ids := make([]string, 0, len(paths))
	for _, p := range paths {
		id, err := uploadAttachFile(uploadClient, grant.UploadURL, p)
		if err != nil {
			return nil, err
		}
		if !c.jsonOut {
			_, _ = fmt.Fprintf(out, "uploaded %s -> %s\n", p, id)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// maxUploadResponse bounds how much of depot's upload response wagon reads.
const maxUploadResponse = 64 << 10

// uploadAttachFile streams path's bytes to a depot grant's upload_url. No
// client certificate is sent: the grant token embedded in the URL is the
// credential, and the request goes to depot, not to rail.
func uploadAttachFile(hc *http.Client, uploadURL, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", &usageError{fmt.Sprintf("--attach-file %s: %v", path, err)}
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", &usageError{fmt.Sprintf("--attach-file %s: %v", path, err)}
	}
	// Not classifyTransportErr: this leg sends no client certificate, so a
	// TLS failure here is depot's server certificate, never rail's.
	if info.Size() <= 0 {
		return "", &netError{fmt.Errorf("depot: upload size must be > 0")}
	}

	name := filepath.Base(path)
	contentType := cmp.Or(mime.TypeByExtension(filepath.Ext(name)), "application/octet-stream")

	q := url.Values{"filename": {name}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, uploadURL+"?"+q.Encode(), f)
	if err != nil {
		return "", &netError{fmt.Errorf("depot: build grant upload request: %w", err)}
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = info.Size()

	resp, err := hc.Do(req)
	if err != nil {
		return "", &netError{fmt.Errorf("depot: grant upload: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUploadResponse))
	if err != nil {
		return "", &netError{fmt.Errorf("depot: read response body: %w", err)}
	}
	// depot answers errors in rail's {"error","message"} shape, so the same
	// exit-code mapping applies to a depot-leg failure.
	if resp.StatusCode != http.StatusCreated {
		return "", newAPIError(resp.StatusCode, data)
	}
	var uploaded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &uploaded); err != nil {
		return "", &netError{fmt.Errorf("depot: decode response: %w", err)}
	}
	return uploaded.ID, nil
}
