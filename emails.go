package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"slices"
)

// emailCheckResponse contains the assessment fields shown in human output.
// JSON output preserves the complete server response.
type emailCheckResponse struct {
	Email      string `json:"email"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	ValidAsOf  string `json:"valid_as_of"`
	ValidUntil string `json:"valid_until"`
	Domain     struct {
		HasMX          bool   `json:"has_mx"`
		Routing        string `json:"routing"`
		Reason         string `json:"reason"`
		Classification struct {
			Category string `json:"category"`
		} `json:"classification"`
	} `json:"domain"`
	SMTP struct {
		Result            string `json:"result"`
		Reason            string `json:"reason"`
		RecipientRejected *bool  `json:"recipient_rejected"`
		CheckedAt         string `json:"checked_at"`
		NextProbeAt       string `json:"next_probe_at"`
	} `json:"smtp"`
	Evidence struct {
		Source          string `json:"source"`
		LastDeliveredAt string `json:"last_delivered_at"`
	} `json:"evidence"`
}

func runEmails(c *client, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return &usageError{"usage: emails <check> [args]"}
	}
	if args[0] != "check" {
		return &usageError{fmt.Sprintf("unknown emails subcommand %q", args[0])}
	}
	fs := flag.NewFlagSet("emails check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	force := fs.Bool("force", false, "retry an eligible cached SMTP probe")
	if err := parseDomainFlags(fs, args[1:], "EMAIL"); err != nil {
		return err
	}
	req := struct {
		Email string `json:"email"`
		Force bool   `json:"force,omitempty"`
	}{Email: args[1], Force: *force}
	var resp emailCheckResponse
	raw, err := c.do(http.MethodPost, "/api/v1/emails/check", req, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	rejected := "unknown"
	if resp.SMTP.RecipientRejected != nil {
		rejected = fmt.Sprintf("%t", *resp.SMTP.RecipientRejected)
	}
	fields := [][2]string{
		{"email", resp.Email},
		{"status", resp.Status},
		{"reason", resp.Reason},
		{"valid_as_of", orDash(resp.ValidAsOf)},
		{"valid_until", orDash(resp.ValidUntil)},
		{"has_mx", fmt.Sprintf("%t", resp.Domain.HasMX)},
		{"routing", resp.Domain.Routing},
		{"routing_reason", orDash(resp.Domain.Reason)},
		{"category", resp.Domain.Classification.Category},
		{"smtp_result", resp.SMTP.Result},
		{"smtp_reason", resp.SMTP.Reason},
		{"recipient_rejected", rejected},
		{"smtp_checked_at", orDash(resp.SMTP.CheckedAt)},
		{"next_probe_at", orDash(resp.SMTP.NextProbeAt)},
		{"evidence_source", resp.Evidence.Source},
		{"last_delivered_at", orDash(resp.Evidence.LastDeliveredAt)},
	}
	if resp.Status == "unknown" && resp.Reason == "recipient_accepted" && resp.SMTP.Result == "accepted" {
		fields = slices.Insert(fields, 3, [2]string{"assessment", "SMTP accepted the recipient; mailbox existence is unconfirmed."})
	}
	renderKV(out, fields)
	return nil
}
