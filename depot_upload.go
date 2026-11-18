package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const depotTransferTimeout = 5 * time.Minute
const depotTusVersion = "1.0.0"
const depotTusChunkSize = 8 << 20
const depotTusDigestKey = "wagon-sha256"

func runDepotUpload(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("depot upload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: wagon depot upload [flags] FILE")
		fs.PrintDefaults()
	}
	ttl := fs.String("ttl", "", "file retention duration")
	exportable := fs.Bool("exportable", false, "allow dashboard sharing")
	contentType := fs.String("content-type", "", "media type (default: detect from extension)")
	resumable := fs.Bool("resumable", false, "start a resumable upload")
	resume := fs.String("resume", "", "continue an existing resumable upload by ID")
	if err := parseDepotFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return &usageError{"usage: depot upload [flags] FILE"}
	}
	seen := visitedFlags(fs)
	if seen["resume"] {
		if !depotFileID.MatchString(*resume) {
			return &usageError{"invalid resumable upload ID"}
		}
		if seen["resumable"] || seen["ttl"] || seen["content-type"] || seen["exportable"] {
			return &usageError{"--resume cannot be combined with creation flags"}
		}
	}

	filename := fs.Arg(0)
	info, err := os.Stat(filename)
	if err != nil {
		return &usageError{fmt.Sprintf("inspect upload file: %v", err)}
	}
	if !info.Mode().IsRegular() {
		return &usageError{"depot upload requires a regular file"}
	}
	file, err := os.Open(filename)
	if err != nil {
		return &usageError{fmt.Sprintf("open upload file: %v", err)}
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return &usageError{"inspect open upload file: expected a regular file"}
	}

	upload := *c
	hc := *c.http
	hc.Timeout = depotTransferTimeout
	upload.http = &hc
	size := info.Size()
	if *resume != "" {
		return resumeDepotUpload(&upload, file, size, *resume, out, stderr)
	}

	name := filepath.Base(filename)
	mediaType := cmp.Or(*contentType, mime.TypeByExtension(filepath.Ext(name)), "application/octet-stream")
	if _, _, err := mime.ParseMediaType(mediaType); err != nil || strings.ContainsAny(mediaType, "\r\n") {
		return &usageError{"invalid --content-type"}
	}
	q := url.Values{}
	if *ttl != "" {
		q.Set("ttl", *ttl)
	}
	if seen["exportable"] {
		q.Set("exportable", strconv.FormatBool(*exportable))
	}
	if *resumable {
		return createDepotUpload(&upload, file, size, name, mediaType, q, out, stderr)
	}
	return putDepotFile(&upload, file, size, name, mediaType, q, out, stderr)
}

func putDepotFile(c *client, file *os.File, size int64, name, mediaType string, q url.Values, out, stderr io.Writer) error {
	q.Set("filename", name)
	req, err := newDepotRequest(c, http.MethodPut, depotFilesPath+"?"+q.Encode(), depotFileSection(file, 0, size))
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", mediaType)
	_, raw, err := doDepot(c, req, http.StatusCreated)
	var uploaded depotFile
	if err != nil {
		if json.Unmarshal(raw, &uploaded) == nil && depotFileID.MatchString(uploaded.ID) {
			_, _ = fmt.Fprintf(stderr, "file ID: %s\n", uploaded.ID)
		}
		return err
	}
	if err := json.Unmarshal(raw, &uploaded); err != nil {
		return fmt.Errorf("depot: decode uploaded file: %w", err)
	}
	if !depotFileID.MatchString(uploaded.ID) {
		return fmt.Errorf("depot: invalid uploaded file ID")
	}
	return printDepotFile(c, out, uploaded, raw)
}

func createDepotUpload(c *client, file *os.File, size int64, name, mediaType string, q url.Values, out, stderr io.Writer) error {
	digest, err := depotUploadDigest(file, size)
	if err != nil {
		return err
	}
	path := depotFilesPath
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	req, err := newDepotRequest(c, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Tus-Resumable", depotTusVersion)
	req.Header.Set("Upload-Length", strconv.FormatInt(size, 10))
	req.Header.Set("Upload-Metadata", "filename "+base64.StdEncoding.EncodeToString([]byte(name))+
		",filetype "+base64.StdEncoding.EncodeToString([]byte(mediaType))+","+depotTusDigestKey+" "+digest)
	headers, _, err := doDepot(c, req, http.StatusCreated)

	id, hasID := strings.CutPrefix(headers.Get("Location"), depotFilesPath+"/")
	if hasID && depotFileID.MatchString(id) {
		_, _ = fmt.Fprintf(stderr, "upload ID: %s\n", id)
	} else if err == nil {
		return fmt.Errorf("depot: invalid resumable upload location")
	}
	if err != nil {
		return err
	}
	if offset, ok := depotTusNumber(headers.Get("Upload-Offset")); !ok || offset != 0 {
		return fmt.Errorf("depot: invalid resumable upload creation offset")
	}
	return finishDepotUpload(c, file, depotFilesPath+"/"+id, 0, size, out)
}

func resumeDepotUpload(c *client, file *os.File, size int64, id string, out, stderr io.Writer) error {
	_, _ = fmt.Fprintf(stderr, "upload ID: %s\n", id)
	path := depotFilesPath + "/" + id
	req, err := newDepotRequest(c, http.MethodHead, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Tus-Resumable", depotTusVersion)
	headers, _, err := doDepot(c, req, http.StatusNoContent)
	if err != nil {
		return err
	}
	length, ok := depotTusNumber(headers.Get("Upload-Length"))
	if !ok {
		return fmt.Errorf("depot: invalid resumable upload length")
	}
	if length != size {
		return &usageError{fmt.Sprintf("upload expects %d bytes but local file has %d bytes", length, size)}
	}
	offset, ok := depotTusNumber(headers.Get("Upload-Offset"))
	if !ok || offset > length {
		return fmt.Errorf("depot: invalid resumable upload offset")
	}
	if err := verifyDepotUploadFile(file, size, headers.Get("Upload-Metadata")); err != nil {
		return err
	}
	if offset == length {
		// A failed final scan can leave every byte stored with finalization pending.
		uploaded, raw, err := getDepotFile(c, path)
		if err != nil {
			return err
		}
		if uploaded.State != "pending_upload" {
			return printCompletedDepotUpload(c, out, uploaded, raw)
		}
		if err := patchDepotUpload(c, file, path, offset, 0); err != nil {
			return err
		}
	}
	return finishDepotUpload(c, file, path, offset, length, out)
}

func depotUploadDigest(file *os.File, size int64) (string, error) {
	hash := sha256.New()
	n, err := io.Copy(hash, io.NewSectionReader(file, 0, size))
	if err != nil {
		return "", fmt.Errorf("hash upload file: %w", err)
	}
	if n != size {
		return "", &usageError{"upload file size changed while hashing"}
	}
	return base64.StdEncoding.EncodeToString(hash.Sum(nil)), nil
}

func verifyDepotUploadFile(file *os.File, size int64, metadata string) error {
	fields := make(map[string]string)
	for entry := range strings.SplitSeq(metadata, ",") {
		key, value, _ := strings.Cut(entry, " ")
		fields[key] = value
	}
	if digest, ok := fields[depotTusDigestKey]; ok {
		got, err := depotUploadDigest(file, size)
		if err != nil {
			return err
		}
		if got != digest {
			return &usageError{"local file does not match the upload checksum"}
		}
		return nil
	}
	if encoded := fields["filename"]; encoded != "" {
		name, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("depot: invalid resumable upload filename metadata")
		}
		if string(name) != filepath.Base(file.Name()) {
			return &usageError{"local filename does not match the upload metadata"}
		}
	}
	return nil
}

func finishDepotUpload(c *client, file *os.File, path string, offset, length int64, out io.Writer) error {
	for offset < length {
		size := min(length-offset, depotTusChunkSize)
		if err := patchDepotUpload(c, file, path, offset, size); err != nil {
			return err
		}
		offset += size
	}
	uploaded, raw, err := getDepotFile(c, path)
	if err != nil {
		return err
	}
	return printCompletedDepotUpload(c, out, uploaded, raw)
}

func printCompletedDepotUpload(c *client, out io.Writer, file depotFile, raw []byte) error {
	switch {
	case file.Verdict == "infected":
		return &apiError{status: http.StatusUnprocessableEntity, code: "infected", message: "file rejected: malware detected"}
	case file.State == "pending_upload":
		return fmt.Errorf("depot: upload is still pending finalization")
	}
	return printDepotFile(c, out, file, raw)
}

func patchDepotUpload(c *client, file *os.File, path string, offset, size int64) error {
	req, err := newDepotRequest(c, http.MethodPatch, path, depotFileSection(file, offset, size))
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Tus-Resumable", depotTusVersion)
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	headers, _, err := doDepot(c, req, http.StatusNoContent)
	if err != nil {
		return err
	}
	if next, ok := depotTusNumber(headers.Get("Upload-Offset")); !ok || next != offset+size {
		return fmt.Errorf("depot: invalid offset after resumable upload chunk")
	}
	return nil
}

func depotFileSection(file *os.File, offset, size int64) io.Reader {
	if size == 0 {
		return nil
	}
	return io.NewSectionReader(file, offset, size)
}

func depotTusNumber(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n >= 0 && s[0] >= '0' && s[0] <= '9'
}
