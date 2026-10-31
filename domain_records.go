package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type domainRecordsResponse struct {
	Domain           string            `json:"domain"`
	DNSMode          string            `json:"dns_mode"`
	ReceivingEnabled *bool             `json:"receiving_enabled"`
	Records          []domainDNSRecord `json:"records"`
}

type domainCheckResult struct {
	Check    string `json:"check"`
	Subject  string `json:"subject"`
	Status   string `json:"status"`
	Observed string `json:"observed,omitempty"`
	Want     string `json:"want,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type domainCheckResponse struct {
	Domain           string              `json:"domain"`
	DNSMode          string              `json:"dns_mode"`
	ReceivingEnabled *bool               `json:"receiving_enabled"`
	CheckedAt        string              `json:"checked_at"`
	Results          []domainCheckResult `json:"results"`
}

type dnsValidationError struct{ incomplete bool }

func (e *dnsValidationError) Error() string {
	if e.incomplete {
		return "DNS validation incomplete; see validation results"
	}
	return "DNS validation failed; see validation results"
}

func runDomainRecords(c *client, domain string, out io.Writer) error {
	var records domainRecordsResponse
	raw, err := c.do(http.MethodGet, domainPath(domain)+"/records", nil, &records)
	if err != nil {
		return err
	}
	if !c.jsonOut {
		if records.ReceivingEnabled != nil {
			_, _ = fmt.Fprintf(out, "Usage: %s\n", domainUsage(*records.ReceivingEnabled))
		}
		_, _ = fmt.Fprintln(out, "DNS guidance:")
		renderDomainGuidance(out, records.Records)
		_, _ = fmt.Fprintln(out, "\nMerge SPF guidance into your existing SPF record. Keep other sending providers.")
		_, _ = fmt.Fprintln(out, "Keep a valid existing DMARC policy and report destination.")
		_, _ = fmt.Fprintln(out, "\nDNS validation (rail resolver):")
	}
	var check domainCheckResponse
	checkRaw, checkErr := c.do(http.MethodPost, domainPath(domain)+"/check", nil, &check)
	if checkErr != nil {
		if api, ok := errors.AsType[*apiError](checkErr); ok && (api.status == http.StatusNotFound || api.status == http.StatusMethodNotAllowed || api.status == http.StatusNotImplemented) {
			checkErr = fmt.Errorf("DNS validation unavailable; upgrade rail to support domain checks, or retry if the domain changed: %w", checkErr)
		}
	} else {
		checkErr = validateDomainCheck(records, check)
	}
	if c.jsonOut {
		if err := printDomainRecordReport(out, raw, checkRaw, checkErr); err != nil {
			return err
		}
	} else if len(check.Results) > 0 {
		rows := make([][]string, 0, len(check.Results))
		for _, result := range check.Results {
			detail := strings.TrimSpace(strings.Join([]string{result.Observed, result.Detail}, " "))
			rows = append(rows, []string{result.Check, result.Subject, result.Status, orDash(detail)})
		}
		renderTable(out, []string{"CHECK", "SUBJECT", "STATUS", "OBSERVED / DETAIL"}, rows)
	}
	return checkErr
}

func validateDomainCheck(records domainRecordsResponse, check domainCheckResponse) error {
	if records.Domain == "" || (records.DNSMode != "records" && records.DNSMode != "delegated") || records.ReceivingEnabled == nil {
		return fmt.Errorf("DNS validation incomplete: records metadata is missing; upgrade rail to support domain checks")
	}
	if check.Domain != records.Domain || check.DNSMode != records.DNSMode || check.ReceivingEnabled == nil || *check.ReceivingEnabled != *records.ReceivingEnabled {
		return fmt.Errorf("DNS validation incomplete: domain settings changed or response metadata is missing; retry the command")
	}
	if _, err := time.Parse(time.RFC3339Nano, check.CheckedAt); err != nil {
		return fmt.Errorf("DNS validation incomplete: invalid checked_at: %w", err)
	}
	if len(check.Results) == 0 {
		return fmt.Errorf("DNS validation incomplete: server returned no results")
	}
	var failed, incomplete bool
	for _, result := range check.Results {
		if result.Check == "" || result.Subject == "" {
			incomplete = true
		}
		switch result.Status {
		case "pass", "warn", "skip":
		case "fail":
			failed = true
		default:
			incomplete = true
		}
	}
	if failed || incomplete {
		return &dnsValidationError{incomplete: incomplete}
	}
	return nil
}

// Preserve unknown API fields and full numeric precision in both responses.
// Validation errors accompany the report instead of discarding DNS guidance.
func printDomainRecordReport(out io.Writer, records, check []byte, checkErr error) error {
	var report map[string]json.RawMessage
	if err := json.Unmarshal(records, &report); err != nil {
		return fmt.Errorf("decode records: %w", err)
	}
	if report == nil {
		return fmt.Errorf("decode records: expected an object")
	}
	if len(check) == 0 {
		check = []byte("null")
	}
	report["validation"] = check
	if checkErr != nil {
		detail, err := json.Marshal(checkErr.Error())
		if err != nil {
			return fmt.Errorf("encode validation error: %w", err)
		}
		report["validation_error"] = detail
	}
	combined, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode records report: %w", err)
	}
	return printPretty(out, combined)
}

func renderDomainGuidance(out io.Writer, records []domainDNSRecord) {
	rows := make([][]string, 0, len(records))
	for _, record := range records {
		rows = append(rows, []string{record.Name, record.Type, orDash(record.Purpose), orDash(record.Requirement), record.Value})
	}
	renderTable(out, []string{"NAME", "TYPE", "PURPOSE", "REQUIREMENT", "VALUE"}, rows)
}
