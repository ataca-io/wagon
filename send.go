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
)

// sendRequest mirrors httpd's sendRequest (POST /api/v1/send).
type sendRequest struct {
	RequestID      string   `json:"request_id"`
	From           string   `json:"from"`
	To             []string `json:"to"`
	Cc             []string `json:"cc,omitempty"`
	Subject        string   `json:"subject"`
	BodyText       string   `json:"body_text,omitempty"`
	BodyHTML       string   `json:"body_html,omitempty"`
	ReplyTo        string   `json:"reply_to,omitempty"`
	UnsubscribeURL string   `json:"unsubscribe_url,omitempty"`
	Attachments    []string `json:"attachments,omitempty"`
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
	var to, cc, attachFiles repeatedFlag
	fs.Var(&to, "to", "recipient address (repeatable, required)")
	fs.Var(&cc, "cc", "CC recipient address (repeatable)")
	fs.Var(&attachFiles, "attach-file", "local file path to upload as an attachment (repeatable)")
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

	reqID := *requestID
	if reqID == "" {
		reqID = randomRequestID()
	}

	// A grant-and-upload failure must abort before the send, so the message
	// never goes out with a partial attachment set.
	uploadedIDs, err := resolveAttachFiles(c, attachFiles, out)
	if err != nil {
		return err
	}

	return postSend(c, sendRequest{
		RequestID:      reqID,
		From:           sender,
		To:             to,
		Cc:             cc,
		Subject:        *subject,
		BodyText:       bodyText,
		BodyHTML:       *html,
		ReplyTo:        *replyTo,
		UnsubscribeURL: *unsubURL,
		Attachments:    uploadedIDs,
	}, out)
}

// postSend sends req to POST /api/v1/send and prints the response.
func postSend(c *client, req sendRequest, out io.Writer) error {
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
