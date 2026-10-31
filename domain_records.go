package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const domainDNSTimeout = 5 * time.Second

type domainResolver interface {
	LookupTXT(context.Context, string) ([]string, error)
	LookupMX(context.Context, string) ([]*net.MX, error)
	LookupNS(context.Context, string) ([]*net.NS, error)
}

type domainRecordValidation struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Expected string   `json:"expected"`
	Observed []string `json:"observed"`
	Status   string   `json:"status"`
	Error    string   `json:"error,omitempty"`
}

type dnsValidationError struct{ lookupFailed bool }

func (e *dnsValidationError) Error() string {
	if e.lookupFailed {
		return "DNS validation incomplete; see validation results"
	}
	return "DNS records do not match; see validation results"
}

func runDomainRecords(c *client, domain string, out io.Writer, resolver domainResolver) error {
	var resp struct {
		Records []domainDNSRecord `json:"records"`
	}
	raw, err := c.do(http.MethodGet, domainPath(domain)+"/records", nil, &resp)
	if err != nil {
		return err
	}
	if !c.jsonOut {
		_, _ = fmt.Fprintln(out, "Expected DNS records:")
		renderDomainRecords(out, resp.Records)
		_, _ = fmt.Fprintln(out, "\nDNS validation (system resolver):")
	}
	results := validateDomainRecords(resolver, resp.Records)
	if c.jsonOut {
		var report map[string]json.RawMessage
		if err := json.Unmarshal(raw, &report); err != nil {
			return fmt.Errorf("decode records: %w", err)
		}
		if report == nil {
			return fmt.Errorf("decode records: expected an object")
		}
		validation, err := json.Marshal(results)
		if err != nil {
			return fmt.Errorf("encode DNS validation: %w", err)
		}
		report["validation"] = validation
		combined, err := json.Marshal(report)
		if err != nil {
			return fmt.Errorf("encode records report: %w", err)
		}
		if err := printPretty(out, combined); err != nil {
			return err
		}
	} else {
		rows := make([][]string, 0, len(results))
		for _, r := range results {
			detail := strings.Join(r.Observed, " | ")
			if r.Error != "" {
				detail = r.Error
			}
			rows = append(rows, []string{r.Name, r.Type, r.Status, orDash(detail)})
		}
		renderTable(out, []string{"NAME", "TYPE", "STATUS", "OBSERVED / ERROR"}, rows)
		if len(results) == 0 {
			_, _ = fmt.Fprintln(out, "No DNS records to validate.")
		}
	}
	var failed, lookupFailed bool
	for _, r := range results {
		failed = failed || r.Status != "match"
		lookupFailed = lookupFailed || r.Status == "error"
	}
	if failed {
		return &dnsValidationError{lookupFailed: lookupFailed}
	}
	return nil
}

// validateDomainRecords checks each expected value against a DNS answer.
// Shared name/type lookups reuse one answer so a report stays consistent.
func validateDomainRecords(resolver domainResolver, records []domainDNSRecord) []domainRecordValidation {
	type query struct{ name, kind string }
	type answer struct {
		values []string
		err    error
	}
	cache := make(map[query]answer)
	results := make([]domainRecordValidation, 0, len(records))
	for _, record := range records {
		q := query{name: dnsHostname(record.Name) + ".", kind: strings.ToUpper(record.Type)}
		a, ok := cache[q]
		if !ok {
			ctx, cancel := context.WithTimeout(context.Background(), domainDNSTimeout)
			a.values, a.err = lookupDomainRecord(ctx, resolver, q.name, q.kind)
			cancel()
			slices.Sort(a.values)
			cache[q] = a
		}
		result := domainRecordValidation{
			Name: record.Name, Type: record.Type, Expected: record.Value,
			Observed: a.values, Status: "missing",
		}
		if a.err != nil {
			if dnsErr, ok := errors.AsType[*net.DNSError](a.err); !ok || !dnsErr.IsNotFound {
				result.Status = "error"
				result.Error = a.err.Error()
			}
		} else if len(a.values) > 0 {
			result.Status = "mismatch"
			for _, value := range a.values {
				if dnsRecordValue(q.kind, value) == dnsRecordValue(q.kind, record.Value) {
					result.Status = "match"
					break
				}
			}
		}
		results = append(results, result)
	}
	return results
}

func lookupDomainRecord(ctx context.Context, resolver domainResolver, name, kind string) ([]string, error) {
	values := make([]string, 0)
	switch kind {
	case "TXT":
		// LookupTXT joins chunks within each TXT record, including long DKIM keys.
		txt, err := resolver.LookupTXT(ctx, name)
		return append(values, txt...), err
	case "MX":
		mx, err := resolver.LookupMX(ctx, name)
		for _, r := range mx {
			values = append(values, fmt.Sprintf("%d %s", r.Pref, r.Host))
		}
		return values, err
	case "NS":
		ns, err := resolver.LookupNS(ctx, name)
		for _, r := range ns {
			values = append(values, r.Host)
		}
		return values, err
	default:
		return values, fmt.Errorf("unsupported DNS record type %q", kind)
	}
}

func dnsHostname(value string) string {
	return strings.ToLower(strings.TrimSuffix(value, "."))
}

// TXT values match exactly. Hostnames ignore case and a trailing dot;
// MX values must also have the same numeric priority.
func dnsRecordValue(kind, value string) string {
	switch kind {
	case "NS":
		return dnsHostname(value)
	case "MX":
		fields := strings.Fields(value)
		if len(fields) == 2 {
			if priority, err := strconv.ParseUint(fields[0], 10, 16); err == nil {
				return fmt.Sprintf("%d %s", priority, dnsHostname(fields[1]))
			}
		}
	}
	return value
}
