package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

type domainForwardResponse struct {
	LocalPart           string `json:"local_part"`
	Destination         string `json:"destination"`
	Enabled             bool   `json:"enabled"`
	FromMunging         string `json:"from_munging"`
	ConsecutiveFailures int64  `json:"consecutive_failures"`
	DisabledAt          string `json:"disabled_at"`
	DisabledReason      string `json:"disabled_reason"`
	CreatedAt           string `json:"created_at"`
}

func runDomainForwards(c *client, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return &usageError{"usage: domains forwards <list|create|update|delete> DOMAIN [args]"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runDomainForwardsList(c, rest, out, stderr)
	case "create", "update", "delete":
		return runDomainForwardChange(c, sub, rest, out, stderr)
	default:
		return &usageError{fmt.Sprintf("unknown domains forwards subcommand %q", sub)}
	}
}

func runDomainForwardsList(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("domains forwards list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("limit", 100, "page size (1-100)")
	after := fs.String("after", "", "cursor from the previous page's next_cursor")
	if err := parseDomainFlags(fs, args, "DOMAIN"); err != nil {
		return err
	}
	query, err := domainPageQuery(fs, *limit, *after)
	if err != nil {
		return err
	}
	var resp struct {
		Forwards   []domainForwardResponse `json:"forwards"`
		NextCursor string                  `json:"next_cursor"`
	}
	raw, err := c.do(http.MethodGet, domainPath(args[0])+"/forwards"+query, nil, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	rows := make([][]string, 0, len(resp.Forwards))
	for _, f := range resp.Forwards {
		rows = append(rows, []string{
			f.LocalPart, f.Destination, strconv.FormatBool(f.Enabled), orDash(f.FromMunging),
			strconv.FormatInt(f.ConsecutiveFailures, 10), orDash(f.DisabledAt), orDash(f.DisabledReason), f.CreatedAt,
		})
	}
	renderTable(out, []string{"LOCAL_PART", "DESTINATION", "ENABLED", "FROM_MUNGING", "FAILURES", "DISABLED_AT", "DISABLED_REASON", "CREATED"}, rows)
	if resp.NextCursor != "" {
		_, _ = fmt.Fprintf(out, "next_cursor: %s (pass as --after)\n", resp.NextCursor)
	}
	return nil
}

func runDomainForwardChange(c *client, action string, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("domains forwards "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var munging string
	var enabled bool
	if action != "delete" {
		fs.StringVar(&munging, "from-munging", "", "From rewriting: never, always, auto, arc, or empty for the server default")
	}
	if action == "update" {
		fs.BoolVar(&enabled, "enabled", false, "enable or disable the rule (--enabled=true|false)")
	}
	if err := parseDomainFlags(fs, args, "DOMAIN", "LOCAL_PART", "DESTINATION"); err != nil {
		return err
	}
	switch munging {
	case "", "never", "always", "auto", "arc":
	default:
		return &usageError{"--from-munging must be never, always, auto, arc, or empty"}
	}
	path := domainPath(args[0]) + "/forwards"
	if action != "create" {
		path += "/" + url.PathEscape(args[1]) + "/" + url.PathEscape(args[2])
	}
	if action == "delete" {
		if _, err := c.do(http.MethodDelete, path, nil, nil); err != nil {
			return err
		}
		if !c.jsonOut {
			_, _ = fmt.Fprintf(out, "deleted forward %s@%s -> %s\n", args[1], args[0], args[2])
		}
		return nil
	}
	if action == "create" {
		req := struct {
			LocalPart   string `json:"local_part"`
			Destination string `json:"destination"`
			FromMunging string `json:"from_munging"`
		}{LocalPart: args[1], Destination: args[2], FromMunging: munging}
		var resp domainForwardResponse
		raw, err := c.do(http.MethodPost, path, req, &resp)
		if err != nil {
			return err
		}
		if c.jsonOut {
			return printPretty(out, raw)
		}
		renderDomainForward(out, &resp)
		return nil
	}

	seen := visitedFlags(fs)
	req := make(map[string]any)
	if seen["enabled"] {
		req["enabled"] = enabled
	}
	if seen["from-munging"] {
		req["from_munging"] = munging
	}
	if len(req) == 0 {
		return &usageError{"domains forwards update requires at least one change"}
	}
	var resp struct {
		Forward               domainForwardResponse `json:"forward"`
		SuppressionCleared    bool                  `json:"suppression_cleared"`
		StillSuppressedReason string                `json:"still_suppressed_reason"`
	}
	raw, err := c.do(http.MethodPatch, path, req, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	renderDomainForward(out, &resp.Forward)
	renderKV(out, [][2]string{
		{"suppression_cleared", strconv.FormatBool(resp.SuppressionCleared)},
		{"still_suppressed_reason", orDash(resp.StillSuppressedReason)},
	})
	return nil
}

func renderDomainForward(out io.Writer, f *domainForwardResponse) {
	renderKV(out, [][2]string{
		{"local_part", f.LocalPart}, {"destination", f.Destination},
		{"enabled", strconv.FormatBool(f.Enabled)}, {"from_munging", orDash(f.FromMunging)},
		{"consecutive_failures", strconv.FormatInt(f.ConsecutiveFailures, 10)},
		{"disabled_at", orDash(f.DisabledAt)}, {"disabled_reason", orDash(f.DisabledReason)},
		{"created_at", f.CreatedAt},
	})
}
