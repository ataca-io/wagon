package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type emailVerificationResponse struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	From       string `json:"from"`
	Purpose    string `json:"purpose"`
	SubjectID  string `json:"subject_id"`
	Status     string `json:"status"`
	ExpiresAt  string `json:"expires_at"`
	ResendAt   string `json:"resend_at"`
	VerifiedAt string `json:"verified_at"`
}

func runEmailVerifications(c *client, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return &usageError{"usage: email-verifications <create|get|confirm> [args]"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return runEmailVerificationsCreate(c, rest, out, stderr)
	case "get", "confirm":
		fs := flag.NewFlagSet("email-verifications "+sub, flag.ContinueOnError)
		fs.SetOutput(stderr)
		var code string
		if sub == "confirm" {
			fs.StringVar(&code, "code", "", "six-digit verification code (required)")
		}
		if err := parseDomainFlags(fs, rest, "ID"); err != nil {
			return err
		}
		path := "/api/v1/email-verifications/" + url.PathEscape(rest[0])
		if sub == "get" {
			return requestEmailVerification(c, http.MethodGet, path, nil, out)
		}
		if len(code) != 6 {
			return &usageError{"--code must contain six digits"}
		}
		for _, digit := range code {
			if digit < '0' || digit > '9' {
				return &usageError{"--code must contain six digits"}
			}
		}
		req := struct {
			Code string `json:"code"`
		}{Code: code}
		return requestEmailVerification(c, http.MethodPost, path+"/confirm", req, out)
	default:
		return &usageError{fmt.Sprintf("unknown email-verifications subcommand %q", sub)}
	}
}

func runEmailVerificationsCreate(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("email-verifications create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	email := fs.String("email", "", "mailbox address to verify (required)")
	from := fs.String("from", "", "sender address (default: the certificate's first sender)")
	purpose := fs.String("purpose", "", "verification purpose, e.g. signup (required)")
	subjectID := fs.String("subject-id", "", "application subject identifier (required)")
	requestID := fs.String("request-id", "", "idempotency key (default: generated)")
	if err := parseDomainFlags(fs, args); err != nil {
		return err
	}
	switch {
	case *email == "":
		return &usageError{"--email is required"}
	case *purpose == "":
		return &usageError{"--purpose is required"}
	case *subjectID == "":
		return &usageError{"--subject-id is required"}
	}
	sender, err := c.from(*from)
	if err != nil {
		return err
	}
	if *requestID == "" {
		*requestID = randomRequestID()
	}
	req := struct {
		Email     string `json:"email"`
		From      string `json:"from"`
		Purpose   string `json:"purpose"`
		SubjectID string `json:"subject_id"`
		RequestID string `json:"request_id"`
	}{Email: *email, From: sender, Purpose: *purpose, SubjectID: *subjectID, RequestID: *requestID}
	return requestEmailVerification(c, http.MethodPost, "/api/v1/email-verifications", req, out)
}

func requestEmailVerification(c *client, method, path string, req any, out io.Writer) error {
	var resp emailVerificationResponse
	raw, err := c.do(method, path, req, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderKV(out, [][2]string{
		{"id", resp.ID},
		{"email", resp.Email},
		{"from", resp.From},
		{"purpose", resp.Purpose},
		{"subject_id", resp.SubjectID},
		{"status", resp.Status},
		{"expires_at", resp.ExpiresAt},
		{"resend_at", resp.ResendAt},
		{"verified_at", orDash(resp.VerifiedAt)},
	})
	return nil
}
