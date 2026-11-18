package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/ataca-io/wagon/internal/buildinfo"
)

const depotFilesPath = "/api/v1/files"

var depotFileID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type depotFile struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	ContentType   string `json:"content_type"`
	Size          int64  `json:"size"`
	ReceivedBytes int64  `json:"received_bytes"`
	State         string `json:"state"`
	Verdict       string `json:"verdict"`
	Signature     string `json:"signature"`
	Servable      bool   `json:"servable"`
	Exportable    bool   `json:"exportable"`
	URL           string `json:"url"`
	CreatedAt     string `json:"created_at"`
	ExpiresAt     string `json:"expires_at"`
}

func runDepot(c *client, args []string, stdout, stderr io.Writer) error {
	const usage = "usage: depot <upload|list|get|progress|download|delete|claim|grant> ..."
	if len(args) == 0 {
		return &usageError{usage}
	}
	if args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprintln(stderr, usage)
		return flag.ErrHelp
	}

	if c != nil {
		owner := *c
		hc := *c.http
		hc.CheckRedirect = stopDepotRedirect
		owner.http = &hc
		c = &owner
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "upload":
		return runDepotUpload(c, rest, stdout, stderr)
	case "list":
		return runDepotList(c, rest, stdout, stderr)
	case "grant":
		return runDepotGrant(c, rest, stdout, stderr)
	case "get", "progress", "download", "delete", "claim":
		return runDepotFile(c, sub, rest, stdout, stderr)
	default:
		return &usageError{usage}
	}
}

func stopDepotRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func parseDepotFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}
	return nil
}

func runDepotList(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("depot list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	before := fs.String("before", "", "cursor from the previous page's next_before")
	if err := parseDepotFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return &usageError{"usage: depot list [--before ID]"}
	}

	path := depotFilesPath
	if *before != "" {
		path += "?" + url.Values{"before": {*before}}.Encode()
	}
	var list struct {
		Files      []depotFile `json:"files"`
		NextBefore string      `json:"next_before"`
	}
	raw, err := depotJSON(c, http.MethodGet, path, nil, http.StatusOK, &list)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	rows := make([][]string, 0, len(list.Files))
	for _, file := range list.Files {
		rows = append(rows, []string{file.ID, file.Filename, file.State, orDash(file.Verdict),
			strconv.FormatInt(file.Size, 10), file.ExpiresAt})
	}
	renderTable(out, []string{"ID", "FILENAME", "STATE", "VERDICT", "BYTES", "EXPIRES"}, rows)
	if list.NextBefore != "" {
		_, _ = fmt.Fprintf(out, "next_before: %s (pass as --before)\n", list.NextBefore)
	}
	return nil
}

func runDepotFile(c *client, sub string, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("depot "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		if sub == "claim" {
			_, _ = fmt.Fprintln(stderr, "Usage: wagon depot claim ID [--ttl DURATION]")
		} else {
			_, _ = fmt.Fprintf(stderr, "Usage: wagon depot %s ID\n", sub)
		}
		fs.PrintDefaults()
	}
	var ttl string
	if sub == "claim" {
		fs.StringVar(&ttl, "ttl", "", "extend the file's retention by this duration")
	}
	if len(args) == 0 {
		fs.Usage()
		return &usageError{"depot " + sub + " requires a file ID"}
	}
	if args[0] == "-h" || args[0] == "--help" {
		return parseDepotFlags(fs, args)
	}
	id := args[0]
	if !depotFileID.MatchString(id) {
		return &usageError{"invalid depot file ID"}
	}
	if err := parseDepotFlags(fs, args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return &usageError{"unexpected arguments after depot " + sub + " ID"}
	}

	path := depotFilesPath + "/" + id
	switch sub {
	case "download":
		return downloadDepotFile(c, path, out)
	case "delete":
		if _, err := depotJSON(c, http.MethodDelete, path, nil, http.StatusNoContent, nil); err != nil {
			return err
		}
		if !c.jsonOut {
			_, _ = fmt.Fprintf(out, "deleted file %s\n", id)
		}
		return nil
	case "progress":
		return depotProgress(c, path, out)
	case "claim":
		var file depotFile
		raw, err := depotJSON(c, http.MethodPost, path+"/claim",
			struct {
				TTL string `json:"ttl"`
			}{TTL: ttl}, http.StatusOK, &file)
		if err != nil {
			return err
		}
		return printDepotFile(c, out, file, raw)
	default:
		file, raw, err := getDepotFile(c, path)
		if err != nil {
			return err
		}
		return printDepotFile(c, out, file, raw)
	}
}

func getDepotFile(c *client, path string) (depotFile, []byte, error) {
	var file depotFile
	raw, err := depotJSON(c, http.MethodGet, path, nil, http.StatusOK, &file)
	if err == nil && path != depotFilesPath+"/"+file.ID {
		return file, nil, fmt.Errorf("depot: wrong file ID in metadata response")
	}
	return file, raw, err
}

func printDepotFile(c *client, out io.Writer, file depotFile, raw []byte) error {
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderKV(out, [][2]string{
		{"id", file.ID}, {"filename", file.Filename}, {"content_type", orDash(file.ContentType)},
		{"size", strconv.FormatInt(file.Size, 10)}, {"received_bytes", strconv.FormatInt(file.ReceivedBytes, 10)},
		{"state", file.State}, {"verdict", orDash(file.Verdict)}, {"signature", orDash(file.Signature)},
		{"servable", strconv.FormatBool(file.Servable)}, {"exportable", strconv.FormatBool(file.Exportable)},
		{"created_at", orDash(file.CreatedAt)}, {"expires_at", orDash(file.ExpiresAt)}, {"url", orDash(file.URL)},
	})
	return nil
}

func depotProgress(c *client, path string, out io.Writer) error {
	var progress struct {
		ID       string `json:"id"`
		Received int64  `json:"received"`
		Total    int64  `json:"total"`
		State    string `json:"state"`
	}
	raw, err := depotJSON(c, http.MethodGet, path+"/progress", nil, http.StatusOK, &progress)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderKV(out, [][2]string{
		{"id", progress.ID}, {"state", progress.State},
		{"received", strconv.FormatInt(progress.Received, 10)}, {"total", strconv.FormatInt(progress.Total, 10)},
	})
	return nil
}

func runDepotGrant(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("depot grant", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ttl := fs.String("ttl", "", "grant lifetime")
	fileTTL := fs.String("file-ttl", "", "retention for files uploaded with this grant")
	maxFiles := fs.Int("max-files", 0, "maximum files this grant accepts")
	maxBytes := fs.Int64("max-bytes", 0, "maximum bytes per file")
	var origins, contentTypes repeatedFlag
	fs.Var(&origins, "origin", "allowed browser origin (repeatable)")
	fs.Var(&contentTypes, "content-type", "allowed media type (repeatable)")
	if err := parseDepotFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *ttl == "" || *maxFiles <= 0 || *maxBytes <= 0 || len(origins) == 0 {
		return &usageError{"usage: depot grant --ttl DURATION --max-files N --max-bytes N --origin ORIGIN [--file-ttl DURATION] [--content-type TYPE]"}
	}
	req := struct {
		TTL          string   `json:"ttl"`
		FileTTL      string   `json:"file_ttl,omitempty"`
		MaxFiles     int      `json:"max_files"`
		MaxBytes     int64    `json:"max_bytes"`
		Origins      []string `json:"origins"`
		ContentTypes []string `json:"content_types,omitempty"`
	}{
		TTL: *ttl, FileTTL: *fileTTL, MaxFiles: *maxFiles, MaxBytes: *maxBytes,
		Origins: origins, ContentTypes: contentTypes,
	}
	var grant struct {
		UploadURL string `json:"upload_url"`
		ExpiresAt string `json:"expires_at"`
	}
	raw, err := depotJSON(c, http.MethodPost, "/api/v1/upload-grants", req, http.StatusCreated, &grant)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderKV(out, [][2]string{{"upload_url", grant.UploadURL}, {"expires_at", grant.ExpiresAt}})
	return nil
}

func downloadDepotFile(c *client, path string, out io.Writer) error {
	if c.jsonOut {
		return &usageError{"depot download writes file bytes and cannot use --json"}
	}
	file, _, err := getDepotFile(c, path)
	if err != nil {
		return err
	}
	if !file.Servable || file.URL == "" {
		return &apiError{status: http.StatusUnprocessableEntity, code: "not_servable", message: "file is not available for download"}
	}
	link, err := url.Parse(file.URL)
	if err != nil || link.Scheme != "https" || link.Hostname() == "" || link.User != nil {
		return fmt.Errorf("depot: invalid HTTPS download URL")
	}

	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: c.caPool}}
	defer transport.CloseIdleConnections()
	hc := &http.Client{Transport: transport, CheckRedirect: stopDepotRedirect, Timeout: depotTransferTimeout}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, link.String(), nil)
	if err != nil {
		return fmt.Errorf("depot: invalid download request")
	}
	setDepotHeaders(req)
	req.Header.Del("Accept")
	resp, err := hc.Do(req)
	if err != nil {
		// net/http transport errors include the full capability URL.
		return fmt.Errorf("depot: download connection failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return newAPIError(resp.StatusCode, nil)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("depot: transfer download: %w", err)
	}
	return nil
}

func newDepotRequest(c *client, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("depot: build %s request: %w", method, err)
	}
	setDepotHeaders(req)
	return req, nil
}

func setDepotHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "wagon/"+buildinfo.Version())
	req.Header.Set("Accept", "application/json")
}

func doDepot(c *client, req *http.Request, expected int) (http.Header, []byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, classifyTransportErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	const maxResponse = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return resp.Header, nil, fmt.Errorf("depot: read response: %w", err)
	}
	if resp.StatusCode != expected {
		if resp.StatusCode < 400 || len(data) > maxResponse {
			data = nil
		}
		return resp.Header, data, newAPIError(resp.StatusCode, data)
	}
	if len(data) > maxResponse {
		return resp.Header, nil, fmt.Errorf("depot: response exceeds 1 MiB")
	}
	return resp.Header, data, nil
}

func depotJSON(c *client, method, path string, body any, status int, out any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("depot: encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := newDepotRequest(c, method, path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	_, data, err := doDepot(c, req, status)
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("depot: decode response: %w", err)
		}
	}
	return data, nil
}
