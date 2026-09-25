package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// formUploads mirrors httpd's formUploadsAPI, the wire shape of a form's
// upload policy.
type formUploads struct {
	MaxFiles     int      `json:"max_files"`
	MaxBytes     int64    `json:"max_bytes,omitempty"`
	ContentTypes []string `json:"content_types,omitempty"`
	TTL          string   `json:"ttl,omitempty"`
}

// formCreateRequest mirrors httpd's formCreateRequest (POST /api/v1/forms).
type formCreateRequest struct {
	Name           string            `json:"name"`
	From           string            `json:"from"`
	To             []string          `json:"to"`
	Subject        string            `json:"subject"`
	RedirectURL    string            `json:"redirect_url,omitempty"`
	AllowedOrigins []string          `json:"allowed_origins,omitempty"`
	RateLimit      int               `json:"rate_limit,omitempty"`
	CcSubmitter    bool              `json:"cc_submitter,omitempty"`
	FieldLabels    map[string]string `json:"field_labels,omitempty"`
	Uploads        *formUploads      `json:"uploads,omitempty"`
}

// formUpdateRequest mirrors httpd's formUpdateRequest (PATCH
// /api/v1/forms/{token}): every field is a pointer, sent only when the
// matching flag was given on the command line.
type formUpdateRequest struct {
	Name           *string            `json:"name,omitempty"`
	To             *[]string          `json:"to,omitempty"`
	Subject        *string            `json:"subject,omitempty"`
	RedirectURL    *string            `json:"redirect_url,omitempty"`
	AllowedOrigins *[]string          `json:"allowed_origins,omitempty"`
	RateLimit      *int               `json:"rate_limit,omitempty"`
	CcSubmitter    *bool              `json:"cc_submitter,omitempty"`
	FieldLabels    *map[string]string `json:"field_labels,omitempty"`
	Enabled        *bool              `json:"enabled,omitempty"`
	Uploads        *formUploads       `json:"uploads,omitempty"`
}

// formResponse mirrors httpd's formAPIResponse.
type formResponse struct {
	Token          string            `json:"token"`
	Name           string            `json:"name"`
	From           string            `json:"from"`
	To             []string          `json:"to"`
	Subject        string            `json:"subject"`
	RedirectURL    string            `json:"redirect_url,omitempty"`
	AllowedOrigins []string          `json:"allowed_origins,omitempty"`
	RateLimit      int               `json:"rate_limit"`
	CcSubmitter    bool              `json:"cc_submitter"`
	Enabled        bool              `json:"enabled"`
	CreatedAt      string            `json:"created_at"`
	FieldLabels    map[string]string `json:"field_labels,omitempty"`
	Uploads        *formUploads      `json:"uploads,omitempty"`
}

// formFieldPair mirrors store.FormFieldPair.
type formFieldPair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// formSubmission mirrors httpd's formSubmissionAPIResponse.
type formSubmission struct {
	ID        string          `json:"id"`
	FormToken string          `json:"form_token"`
	FormName  string          `json:"form_name"`
	MessageID string          `json:"message_id"`
	Fields    []formFieldPair `json:"fields"`
	Subject   string          `json:"subject"`
	ReplyTo   string          `json:"reply_to"`
	Outcome   string          `json:"outcome"`
	CreatedAt string          `json:"created_at"`
}

// formSubmissionsResponse mirrors httpd's formSubmissionsAPIResponse (GET
// /api/v1/forms/{token}/submissions).
type formSubmissionsResponse struct {
	Submissions []formSubmission `json:"submissions"`
	Count       int              `json:"count"`
	NextBefore  string           `json:"next_before,omitempty"`
}

// runForms implements `wagon forms <list|get|create|update|delete|submissions>`.
func runForms(c *client, args []string, out, stderr io.Writer) error {
	if len(args) < 1 {
		return &usageError{"usage: forms <list|get|create|update|delete|submissions> [args]"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runFormsList(c, rest, out, stderr)
	case "get":
		return runFormsGet(c, rest, out)
	case "create":
		return runFormsCreate(c, rest, out, stderr)
	case "update":
		return runFormsUpdate(c, rest, out, stderr)
	case "delete":
		return runFormsDelete(c, rest, out)
	case "submissions":
		return runFormsSubmissions(c, rest, out, stderr)
	default:
		return &usageError{fmt.Sprintf("unknown forms subcommand %q", sub)}
	}
}

func runFormsList(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("forms list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		return &usageError{"forms list takes no arguments"}
	}

	var resp []formResponse
	raw, err := c.do(http.MethodGet, "/api/v1/forms", nil, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	rows := make([][]string, 0, len(resp))
	for _, f := range resp {
		rows = append(rows, []string{f.Token, f.Name, strings.Join(f.To, ","), fmt.Sprintf("%t", f.Enabled), f.CreatedAt})
	}
	renderTable(out, []string{"TOKEN", "NAME", "TO", "ENABLED", "CREATED"}, rows)
	return nil
}

func runFormsGet(c *client, args []string, out io.Writer) error {
	token, err := formTokenArg(args)
	if err != nil {
		return err
	}
	var resp formResponse
	raw, err := c.do(http.MethodGet, "/api/v1/forms/"+url.PathEscape(token), nil, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderForm(out, &resp)
	return nil
}

func renderForm(out io.Writer, f *formResponse) {
	renderKV(out, [][2]string{
		{"token", f.Token},
		{"name", f.Name},
		{"from", f.From},
		{"to", strings.Join(f.To, ", ")},
		{"subject", f.Subject},
		{"redirect_url", orDash(f.RedirectURL)},
		{"allowed_origins", orDash(strings.Join(f.AllowedOrigins, ", "))},
		{"rate_limit", fmt.Sprintf("%d", f.RateLimit)},
		{"cc_submitter", fmt.Sprintf("%t", f.CcSubmitter)},
		{"enabled", fmt.Sprintf("%t", f.Enabled)},
		{"created_at", f.CreatedAt},
	})
}

// formFlags holds the flags formCreateRequest and formUpdateRequest share
// (everything but from, which is create-only and immutable).
type formFlags struct {
	name           *string
	subject        *string
	redirect       *string
	rateLimit      *int
	ccSubmitter    *bool
	uploadMaxFiles *int
	uploadMaxBytes *int64
	uploadTTL      *string
	to             repeatedFlag
	origins        repeatedFlag
	uploadTypes    repeatedFlag
	labels         labelFlag
}

// labelFlag collects a repeatable name=Label flag.
type labelFlag map[string]string

func (l labelFlag) String() string { return "" }

func (l labelFlag) Set(v string) error {
	name, label, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("expected name=Label, got %q", v)
	}
	l[name] = label
	return nil
}

func registerFormFlags(fs *flag.FlagSet) *formFlags {
	ff := &formFlags{
		name:           fs.String("name", "", "human label"),
		subject:        fs.String("subject", "", "default subject"),
		redirect:       fs.String("redirect", "", "post-submit redirect URL"),
		rateLimit:      fs.Int("rate-limit", 0, "submissions/hour (0 = server default)"),
		ccSubmitter:    fs.Bool("cc-submitter", false, "CC the submitter"),
		uploadMaxFiles: fs.Int("upload-max-files", 0, "accept up to N file uploads per submission (0 = off)"),
		uploadMaxBytes: fs.Int64("upload-max-bytes", 0, "per-file size cap in bytes (0 = server default)"),
		uploadTTL:      fs.String("upload-ttl", "", "how long depot keeps uploaded files, e.g. 168h"),
		labels:         make(labelFlag),
	}
	fs.Var(&ff.to, "to", "recipient address (repeatable)")
	fs.Var(&ff.origins, "origin", "allowed origin (repeatable)")
	fs.Var(&ff.uploadTypes, "upload-type", "allowed upload media type (repeatable)")
	fs.Var(ff.labels, "label", "field label as name=Label (repeatable)")
	return ff
}

// uploads builds the wire-shape uploads policy from the flags. Call it only
// when an --upload-* flag was seen; max_files 0 is significant on the wire
// (uploads off) and must still be sent.
func (ff *formFlags) uploads() *formUploads {
	return &formUploads{
		MaxFiles:     *ff.uploadMaxFiles,
		MaxBytes:     *ff.uploadMaxBytes,
		ContentTypes: ff.uploadTypes,
		TTL:          *ff.uploadTTL,
	}
}

// uploadFlagsSeen reports whether any --upload-* flag was visited. The other
// upload flags only mean something alongside --upload-max-files, which picks
// the policy's on/off state, so it is required whenever they are given.
func uploadFlagsSeen(seen map[string]bool) (bool, error) {
	others := seen["upload-max-bytes"] || seen["upload-ttl"] || seen["upload-type"]
	if others && !seen["upload-max-files"] {
		return false, &usageError{"--upload-max-files is required when --upload-ttl, --upload-max-bytes, or --upload-type is set"}
	}
	return seen["upload-max-files"], nil
}

func runFormsCreate(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("forms create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "sender address, must be a cert email SAN (required)")
	ff := registerFormFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		return &usageError{"forms create takes no positional arguments; write boolean flags as --flag=false"}
	}

	switch {
	case *from == "":
		return &usageError{"--from is required"}
	case *ff.name == "":
		return &usageError{"--name is required"}
	case *ff.subject == "":
		return &usageError{"--subject is required"}
	case len(ff.to) == 0:
		return &usageError{"at least one --to is required"}
	}

	hasUploads, err := uploadFlagsSeen(visitedFlags(fs))
	if err != nil {
		return err
	}

	req := formCreateRequest{
		Name:           *ff.name,
		From:           *from,
		To:             ff.to,
		Subject:        *ff.subject,
		RedirectURL:    *ff.redirect,
		AllowedOrigins: ff.origins,
		RateLimit:      *ff.rateLimit,
		CcSubmitter:    *ff.ccSubmitter,
		FieldLabels:    ff.labels,
	}
	if hasUploads {
		req.Uploads = ff.uploads()
	}

	var resp formResponse
	raw, err := c.do(http.MethodPost, "/api/v1/forms", req, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderForm(out, &resp)
	return nil
}

func runFormsUpdate(c *client, args []string, out, stderr io.Writer) error {
	if len(args) < 1 {
		return &usageError{"usage: forms update <token> [flags]"}
	}
	token, rest := args[0], args[1:]

	fs := flag.NewFlagSet("forms update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ff := registerFormFlags(fs)
	enabled := fs.Bool("enabled", false, "enable or disable the form (--enabled=true|false)")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		return &usageError{"forms update takes one token; write boolean flags as --flag=false"}
	}

	seen := visitedFlags(fs)
	hasUploads, err := uploadFlagsSeen(seen)
	if err != nil {
		return err
	}

	var req formUpdateRequest
	if seen["name"] {
		req.Name = ff.name
	}
	if seen["to"] {
		to := []string(ff.to)
		req.To = &to
	}
	if seen["subject"] {
		req.Subject = ff.subject
	}
	if seen["redirect"] {
		req.RedirectURL = ff.redirect
	}
	if seen["origin"] {
		origins := []string(ff.origins)
		req.AllowedOrigins = &origins
	}
	if seen["rate-limit"] {
		req.RateLimit = ff.rateLimit
	}
	if seen["cc-submitter"] {
		req.CcSubmitter = ff.ccSubmitter
	}
	if seen["label"] {
		labels := map[string]string(ff.labels)
		req.FieldLabels = &labels
	}
	if seen["enabled"] {
		req.Enabled = enabled
	}
	if hasUploads {
		req.Uploads = ff.uploads()
	}

	var resp formResponse
	raw, err := c.do(http.MethodPatch, "/api/v1/forms/"+url.PathEscape(token), req, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderForm(out, &resp)
	return nil
}

func runFormsDelete(c *client, args []string, out io.Writer) error {
	token, err := formTokenArg(args)
	if err != nil {
		return err
	}
	var resp map[string]any
	raw, err := c.do(http.MethodDelete, "/api/v1/forms/"+url.PathEscape(token), nil, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	_, _ = fmt.Fprintf(out, "deleted form %s\n", token)
	return nil
}

func runFormsSubmissions(c *client, args []string, out, stderr io.Writer) error {
	if len(args) < 1 {
		return &usageError{"usage: forms submissions <token> [--id ID] [--before ID] [--limit N]"}
	}
	token, rest := args[0], args[1:]

	fs := flag.NewFlagSet("forms submissions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "fetch one submission by id")
	before := fs.String("before", "", "keyset cursor: id of the last row from a previous page")
	limit := fs.Int("limit", 0, "page size, default 50, capped at 100 (0 = server default)")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		return &usageError{"forms submissions takes no positional arguments after the token"}
	}

	path := "/api/v1/forms/" + url.PathEscape(token) + "/submissions"
	if *id != "" {
		path += "/" + url.PathEscape(*id)
		var resp formSubmission
		raw, err := c.do(http.MethodGet, path, nil, &resp)
		if err != nil {
			return err
		}
		if c.jsonOut {
			return printPretty(out, raw)
		}
		renderKV(out, [][2]string{
			{"id", resp.ID},
			{"form_token", resp.FormToken},
			{"form_name", resp.FormName},
			{"message_id", resp.MessageID},
			{"subject", resp.Subject},
			{"reply_to", orDash(resp.ReplyTo)},
			{"outcome", resp.Outcome},
			{"created_at", resp.CreatedAt},
		})
		return nil
	}

	q := url.Values{}
	if *before != "" {
		q.Set("before", *before)
	}
	if *limit > 0 {
		q.Set("limit", strconv.Itoa(*limit))
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var resp formSubmissionsResponse
	raw, err := c.do(http.MethodGet, path, nil, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	rows := make([][]string, 0, len(resp.Submissions))
	for _, s := range resp.Submissions {
		rows = append(rows, []string{s.ID, s.Subject, s.Outcome, s.CreatedAt})
	}
	renderTable(out, []string{"ID", "SUBJECT", "OUTCOME", "CREATED"}, rows)
	if resp.NextBefore != "" {
		_, _ = fmt.Fprintf(out, "next_before: %s\n", resp.NextBefore)
	}
	return nil
}

// formTokenArg requires exactly one positional argument: the form token.
func formTokenArg(args []string) (string, error) {
	if len(args) != 1 {
		return "", &usageError{"expected exactly one form token argument"}
	}
	return args[0], nil
}
