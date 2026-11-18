package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const maxSendAttachments = 20

// sendRequest mirrors httpd's sendRequest (POST /api/v1/send).
type sendRequest struct {
	RequestID      string       `json:"request_id"`
	From           string       `json:"from"`
	To             []string     `json:"to"`
	Cc             []string     `json:"cc,omitempty"`
	Subject        string       `json:"subject"`
	BodyText       string       `json:"body_text,omitempty"`
	BodyHTML       string       `json:"body_html,omitempty"`
	ReplyTo        string       `json:"reply_to,omitempty"`
	UnsubscribeURL string       `json:"unsubscribe_url,omitempty"`
	Attachments    []string     `json:"attachments,omitempty"`
	Files          []inlineFile `json:"files,omitempty"`
}

// sendResponse mirrors httpd's sendResponse.
type sendResponse struct {
	ID         string `json:"id"`
	RequestID  string `json:"request_id"`
	Recipients int    `json:"recipients"`
	Status     string `json:"status"`
}

// runSend implements `wagon send` — POST /api/v1/send.
func runSend(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "sender address (default: the certificate's first sender)")
	subject := fs.String("subject", "", "subject (required)")
	text := fs.String("text", "", "plain-text body (fills body_text)")
	html := fs.String("html", "", "HTML body (fills body_html)")
	bodyFile := fs.String("body-file", "", "read the plain-text body from a file, or - for stdin")
	replyTo := fs.String("reply-to", "", "override Reply-To")
	unsubURL := fs.String("unsubscribe-url", "", "inject List-Unsubscribe headers")
	requestID := fs.String("request-id", "", "idempotency key (default: generated)")
	var to, cc, attachFiles, attachIDs, inlineFiles repeatedFlag
	fs.Var(&to, "to", "recipient address (repeatable, required)")
	fs.Var(&cc, "cc", "CC recipient address (repeatable)")
	fs.Var(&attachFiles, "attach-file", "local file path to upload as an attachment (repeatable)")
	fs.Var(&attachIDs, "attachment-id", "file ID uploaded with a rail grant (repeatable)")
	fs.Var(&inlineFiles, "inline-file", "local file path to send inline through rail (repeatable)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}

	switch {
	case len(to) == 0:
		return &usageError{"at least one --to is required"}
	case *subject == "":
		return &usageError{"--subject is required"}
	case *text != "" && *bodyFile != "":
		return &usageError{"--text and --body-file are mutually exclusive"}
	}

	sender, err := c.from(*from)
	if err != nil {
		return err
	}

	bodyText, err := resolveSendBody(*text, *bodyFile)
	if err != nil {
		return err
	}

	return sendWithAttachments(c, sendRequest{
		RequestID:      *requestID,
		From:           sender,
		To:             to,
		Cc:             cc,
		Subject:        *subject,
		BodyText:       bodyText,
		BodyHTML:       *html,
		ReplyTo:        *replyTo,
		UnsubscribeURL: *unsubURL,
		Attachments:    attachIDs,
	}, attachFiles, inlineFiles, out)
}

func sendWithAttachments(c *client, req sendRequest, uploads, inline []string, out io.Writer) error {
	if len(req.Attachments)+len(uploads)+len(inline) > maxSendAttachments {
		return &usageError{fmt.Sprintf("at most %d attachments are allowed", maxSendAttachments)}
	}
	for _, id := range req.Attachments {
		if !attachmentIDPattern.MatchString(id) {
			return &usageError{fmt.Sprintf("invalid attachment ID %q", id)}
		}
	}

	files, err := readInlineFiles(inline)
	if err != nil {
		return err
	}
	req.Files = files
	receiptKey := req.RequestID
	if req.RequestID == "" {
		req.RequestID = randomRequestID()
	}
	local, err := prepareAttachFiles(c, receiptKey, uploads)
	if err != nil {
		return err
	}

	originalIDs := req.Attachments
	ids, err := resolveAttachFiles(c, local, out)
	if err != nil {
		return err
	}
	req.Attachments = append(originalIDs, ids...)
	err = postSend(c, req, out)
	api, ok := errors.AsType[*apiError](err)
	if !ok || api.status != http.StatusUnprocessableEntity || len(files) > 0 {
		return err
	}
	if api.code != "attachment_not_found" && api.code != "attachment_unavailable" {
		return err
	}

	// Try old receipts first so rail can deduplicate accepted sends even after
	// their original upload TTL has elapsed. Refresh only a rejected send.
	if !clearExpiredReceipts(local, time.Now()) {
		return err
	}
	ids, err = resolveAttachFiles(c, local, out)
	if err != nil {
		return err
	}
	req.Attachments = append(originalIDs, ids...)
	return postSend(c, req, out)
}

// postSend sends req to POST /api/v1/send and prints the response.
func postSend(c *client, req sendRequest, out io.Writer) error {
	if len(req.Attachments)+len(req.Files) > 0 {
		// Inline uploads, scans, and attachment claims can outlast clientTimeout.
		h := *c.http
		h.Timeout = 6 * time.Minute
		withAttachments := *c
		withAttachments.http = &h
		c = &withAttachments
	}
	var resp sendResponse
	raw, err := c.do(http.MethodPost, "/api/v1/send", req, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderKV(out, [][2]string{
		{"id", resp.ID},
		{"request_id", resp.RequestID},
		{"recipients", fmt.Sprintf("%d", resp.Recipients)},
		{"status", resp.Status},
	})
	return nil
}

// resolveSendBody reads the plain-text body from --text or --body-file
// (- for stdin); the caller has already rejected setting both.
func resolveSendBody(text, bodyFile string) (string, error) {
	switch bodyFile {
	case "":
		return text, nil
	case "-":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(data), nil
	default:
		data, err := os.ReadFile(bodyFile)
		if err != nil {
			return "", fmt.Errorf("read body file: %w", err)
		}
		return string(data), nil
	}
}

// randomRequestID generates an idempotency key when --request-id is not
// given: the server requires request_id (api.go), though the API docs call
// it optional.
func randomRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "wagon-" + hex.EncodeToString(b[:])
}
