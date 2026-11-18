package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
)

const (
	attachUploadTimeout = 5 * time.Minute
	attachFileTTL       = "1h"
	maxFilesPerGrant    = 10
	maxUploadResponse   = 64 << 10
)

var attachmentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type attachGrantRequest struct {
	MaxBytes int64  `json:"max_bytes"`
	MaxFiles int    `json:"max_files"`
	FileTTL  string `json:"file_ttl"`
}

type attachFile struct {
	path       string
	size       int64
	digest     []byte
	receipt    string
	id         string
	uploadedAt time.Time
}

type inlineFile struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Content     []byte `json:"content"`
}

func fileContentType(name string) string {
	return cmp.Or(mime.TypeByExtension(filepath.Ext(name)), "application/octet-stream")
}

func attachmentSize(flag, path string) (int64, error) {
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return 0, &usageError{fmt.Sprintf("--%s %s: %v", flag, path, err)}
	case !info.Mode().IsRegular():
		return 0, &usageError{fmt.Sprintf("--%s %s: not a regular file", flag, path)}
	case info.Size() <= 0:
		return 0, &usageError{fmt.Sprintf("--%s %s: file is empty", flag, path)}
	}
	return info.Size(), nil
}

func readInlineFiles(paths []string) ([]inlineFile, error) {
	files := make([]inlineFile, 0, len(paths))
	for _, path := range paths {
		if _, err := attachmentSize("inline-file", path); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, &usageError{fmt.Sprintf("--inline-file %s: %v", path, err)}
		}
		if len(data) == 0 {
			return nil, &usageError{fmt.Sprintf("--inline-file %s: file is empty", path)}
		}
		name := filepath.Base(path)
		files = append(files, inlineFile{Filename: name, ContentType: fileContentType(name), Content: data})
	}
	return files, nil
}

func prepareAttachFiles(c *client, requestID string, paths []string) ([]attachFile, error) {
	files := make([]attachFile, 0, len(paths))
	for _, path := range paths {
		size, err := attachmentSize("attach-file", path)
		if err != nil {
			return nil, err
		}
		files = append(files, attachFile{path: path, size: size})
	}
	if requestID == "" || len(files) == 0 {
		return files, nil
	}
	dir, err := attachCacheDir()
	if err != nil {
		return nil, err
	}
	for i := range files {
		if err := files[i].initReceipt(c, dir, requestID, i); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func resolveAttachFiles(c *client, files []attachFile, out io.Writer) ([]string, error) {
	var pending []*attachFile
	for i := range files {
		if files[i].id == "" {
			pending = append(pending, &files[i])
		}
	}

	if len(pending) != 0 {
		// The upload URL authorizes the request. Only the HTTPS roots are shared
		// with the rail client.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		defer transport.CloseIdleConnections()
		transport.TLSClientConfig = &tls.Config{RootCAs: c.caPool}
		hc := &http.Client{
			Timeout: attachUploadTimeout, Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		for batch := range slices.Chunk(pending, maxFilesPerGrant) {
			if err := uploadAttachBatch(c, hc, batch, out); err != nil {
				return nil, err
			}
		}
	}

	ids := make([]string, 0, len(files))
	for _, f := range files {
		ids = append(ids, f.id)
	}
	return ids, nil
}

func uploadAttachBatch(c *client, hc *http.Client, files []*attachFile, out io.Writer) error {
	var maxSize int64
	for _, f := range files {
		maxSize = max(maxSize, f.size)
	}
	var grant struct {
		UploadURL string `json:"upload_url"`
	}
	if _, err := c.do(http.MethodPost, "/api/v1/upload-grants",
		attachGrantRequest{MaxBytes: maxSize, MaxFiles: len(files), FileTTL: attachFileTTL}, &grant); err != nil {
		return err
	}

	for _, f := range files {
		id, err := uploadAttachFile(hc, grant.UploadURL, f)
		if err != nil {
			return err
		}
		if !c.jsonOut {
			_, _ = fmt.Fprintf(out, "uploaded %s -> %s\n", f.path, id)
		}
		if err := f.saveReceipt(id); err != nil {
			return err
		}
		f.id = id
	}
	return nil
}

func uploadAttachFile(hc *http.Client, uploadURL string, file *attachFile) (string, error) {
	f, err := os.Open(file.path)
	if err != nil {
		return "", &usageError{fmt.Sprintf("--attach-file %s: %v", file.path, err)}
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", &usageError{fmt.Sprintf("--attach-file %s: %v", file.path, err)}
	}
	if !info.Mode().IsRegular() || info.Size() != file.size {
		return "", &usageError{fmt.Sprintf("--attach-file %s changed before upload", file.path)}
	}

	var body io.Reader = f
	sum := sha256.New()
	if file.digest != nil {
		body = io.TeeReader(f, sum)
	}
	name := filepath.Base(file.path)
	u, err := url.Parse(uploadURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", &netError{fmt.Errorf("depot: invalid grant upload URL")}
	}
	q := u.Query()
	q.Set("filename", name)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u.String(), body)
	if err != nil {
		return "", &netError{fmt.Errorf("depot: build grant upload request")}
	}
	req.Header.Set("Content-Type", fileContentType(name))
	req.ContentLength = info.Size()

	resp, err := hc.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return "", &netError{fmt.Errorf("depot: grant upload: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUploadResponse))
	if err != nil {
		return "", &netError{fmt.Errorf("depot: read response body: %w", err)}
	}
	if resp.StatusCode != http.StatusCreated {
		return "", newAPIError(resp.StatusCode, data)
	}
	if file.digest != nil && !bytes.Equal(sum.Sum(nil), file.digest) {
		return "", &usageError{fmt.Sprintf("--attach-file %s changed during upload", file.path)}
	}
	var uploaded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &uploaded); err != nil {
		return "", &netError{fmt.Errorf("depot: decode response: %w", err)}
	}
	if uploaded.ID == "" {
		return "", &netError{fmt.Errorf("depot: upload response has no file ID")}
	}
	return uploaded.ID, nil
}
