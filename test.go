package main

import (
	"errors"
	"flag"
	"io"
	"net/http"
)

// testSendRequest mirrors httpd's testSendRequest (POST /api/v1/test).
type testSendRequest struct {
	To string `json:"to"`
}

// testSendResponse mirrors httpd's testSendResponse.
type testSendResponse struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Status string `json:"status"`
}

// runTest implements `wagon test` — POST /api/v1/test, a one-off test email
// from the certificate's first email SAN. The server requires to (test.go), so
// --to is required.
func runTest(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	to := fs.String("to", "", "recipient address (required)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}
	if *to == "" {
		return &usageError{"--to is required"}
	}

	var resp testSendResponse
	raw, err := c.do(http.MethodPost, "/api/v1/test", testSendRequest{To: *to}, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderKV(out, [][2]string{
		{"id", resp.ID},
		{"from", resp.From},
		{"to", resp.To},
		{"status", resp.Status},
	})
	return nil
}
