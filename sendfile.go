package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// runSendFile implements `wagon send-file` — `send --attach-file` with the
// file paths as arguments, and the subject and body derived from their names.
func runSendFile(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("send-file", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "sender address (default: the certificate's first sender)")
	subject := fs.String("subject", "", "subject (default: the file name, or \"N files\")")
	text := fs.String("text", "", "plain-text body (default: \"Attached: \" and the file names)")
	requestID := fs.String("request-id", "", "idempotency key (default: generated)")
	var to, cc repeatedFlag
	fs.Var(&to, "to", "recipient address (repeatable, required)")
	fs.Var(&cc, "cc", "CC recipient address (repeatable)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: wagon send-file [flags] <file>...")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}

	files := fs.Args()
	switch {
	case len(to) == 0:
		return &usageError{"at least one --to is required"}
	case len(files) == 0:
		return &usageError{"at least one file is required; flags go before the files"}
	}

	sender, err := c.from(*from)
	if err != nil {
		return err
	}

	names := make([]string, len(files))
	for i, f := range files {
		names[i] = filepath.Base(f)
	}
	defaultSubject := names[0]
	if len(names) > 1 {
		defaultSubject = fmt.Sprintf("%d files", len(names))
	}

	uploadedIDs, err := resolveAttachFiles(c, files, out)
	if err != nil {
		return err
	}

	return postSend(c, sendRequest{
		RequestID:   cmp.Or(*requestID, randomRequestID()),
		From:        sender,
		To:          to,
		Cc:          cc,
		Subject:     cmp.Or(*subject, defaultSubject),
		BodyText:    cmp.Or(*text, "Attached: "+strings.Join(names, ", ")),
		Attachments: uploadedIDs,
	}, out)
}
