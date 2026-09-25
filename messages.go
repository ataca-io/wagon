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

// deliveriesResponse mirrors httpd's deliveriesResponse (GET
// /api/v1/messages/{id}/deliveries).
type deliveriesResponse struct {
	MessageID  string              `json:"message_id"`
	Status     string              `json:"status"`
	Deliveries []recipientDelivery `json:"deliveries"`
}

type recipientDelivery struct {
	Recipient    string `json:"recipient"`
	Domain       string `json:"domain"`
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	AttemptedAt  string `json:"attempted_at,omitempty"`
	ResponseCode *int   `json:"response_code,omitempty"`
	Response     string `json:"response,omitempty"`
	Note         string `json:"note,omitempty"`
}

// deliveryResponse renders a delivery's bounce text for the table, prefixed
// with the SMTP response code when the server sent one.
func deliveryResponse(d recipientDelivery) string {
	if d.ResponseCode != nil {
		return fmt.Sprintf("%d %s", *d.ResponseCode, d.Response)
	}
	return orDash(d.Response)
}

// opensResponse mirrors httpd's opensResponse (GET
// /api/v1/messages/{id}/opens).
type opensResponse struct {
	MessageID     string      `json:"message_id"`
	FirstOpenedAt string      `json:"first_opened_at,omitempty"`
	Opens         []openEntry `json:"opens"`
}

type openEntry struct {
	OpenedAt  string `json:"opened_at"`
	SourceIP  string `json:"source_ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
}

// runMessages implements `wagon messages <list|deliveries|opens>`.
func runMessages(c *client, args []string, out io.Writer, stderr io.Writer) error {
	if len(args) < 1 {
		return &usageError{"usage: messages <list|deliveries|opens> [args]"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runMessagesList(c, rest, out, stderr)
	case "deliveries":
		return runMessagesDeliveries(c, rest, out)
	case "opens":
		return runMessagesOpens(c, rest, out)
	default:
		return &usageError{fmt.Sprintf("unknown messages subcommand %q", sub)}
	}
}

// messageListResponse mirrors httpd's messageListResponse (GET
// /api/v1/messages).
type messageListResponse struct {
	Messages   []messageSummary `json:"messages"`
	NextBefore string           `json:"next_before,omitempty"`
}

type messageSummary struct {
	ID            string   `json:"id"`
	RequestID     string   `json:"request_id,omitempty"`
	Status        string   `json:"status"`
	Direction     string   `json:"direction"`
	From          string   `json:"from"`
	To            []string `json:"to"`
	Size          int      `json:"size"`
	CreatedAt     string   `json:"created_at"`
	FirstOpenedAt string   `json:"first_opened_at,omitempty"`
}

// runMessagesList implements `wagon messages list`, a keyset page of the
// client's own messages, newest first.
func runMessagesList(c *client, args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("messages list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	status := fs.String("status", "", "pending, partial, delivered, bounced, or unroutable")
	from := fs.String("from", "", "sender substring")
	to := fs.String("to", "", "recipient substring")
	since := fs.String("since", "", "only messages created at or after this time (RFC3339 or YYYY-MM-DD)")
	until := fs.String("until", "", "only messages created at or before this time")
	limit := fs.Int("limit", 0, "page size (server default 50, maximum 200)")
	before := fs.String("before", "", "continue after this message id (see next_before)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		return &usageError{"messages list takes no positional arguments"}
	}

	// --from and --to name the addresses the way `wagon send` does; the API
	// calls the same two filters sender and recipient.
	q := url.Values{}
	for _, p := range [][2]string{
		{"status", *status}, {"sender", *from}, {"recipient", *to},
		{"since", *since}, {"until", *until}, {"before", *before},
	} {
		if p[1] != "" {
			q.Set(p[0], p[1])
		}
	}
	if *limit > 0 {
		q.Set("limit", strconv.Itoa(*limit))
	}

	path := "/api/v1/messages"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var resp messageListResponse
	raw, err := c.do(http.MethodGet, path, nil, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}

	rows := make([][]string, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		rows = append(rows, []string{
			m.ID, m.CreatedAt, m.Status, m.From,
			orDash(strings.Join(m.To, ", ")), fmt.Sprintf("%d", m.Size),
		})
	}
	renderTable(out, []string{"ID", "CREATED_AT", "STATUS", "FROM", "TO", "SIZE"}, rows)
	if resp.NextBefore != "" {
		_, _ = fmt.Fprintf(out, "\nmore: wagon messages list --before %s\n", resp.NextBefore)
	}
	return nil
}

func runMessagesDeliveries(c *client, args []string, out io.Writer) error {
	id, err := messageIDArg(args)
	if err != nil {
		return err
	}

	var resp deliveriesResponse
	raw, err := c.do(http.MethodGet, "/api/v1/messages/"+url.PathEscape(id)+"/deliveries", nil, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	rows := make([][]string, 0, len(resp.Deliveries))
	for _, d := range resp.Deliveries {
		rows = append(rows, []string{d.Recipient, d.Domain, d.Status, fmt.Sprintf("%d", d.Attempts), orDash(d.AttemptedAt), deliveryResponse(d), orDash(d.Note)})
	}
	renderTable(out, []string{"RECIPIENT", "DOMAIN", "STATUS", "ATTEMPTS", "ATTEMPTED_AT", "RESPONSE", "NOTE"}, rows)
	return nil
}

func runMessagesOpens(c *client, args []string, out io.Writer) error {
	id, err := messageIDArg(args)
	if err != nil {
		return err
	}

	var resp opensResponse
	raw, err := c.do(http.MethodGet, "/api/v1/messages/"+url.PathEscape(id)+"/opens", nil, &resp)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return printPretty(out, raw)
	}
	rows := make([][]string, 0, len(resp.Opens))
	for _, o := range resp.Opens {
		rows = append(rows, []string{o.OpenedAt, orDash(o.SourceIP), orDash(o.UserAgent)})
	}
	renderTable(out, []string{"OPENED_AT", "SOURCE_IP", "USER_AGENT"}, rows)
	return nil
}

// messageIDArg requires exactly one positional argument: the message id.
func messageIDArg(args []string) (string, error) {
	if len(args) != 1 {
		return "", &usageError{"expected exactly one message id argument"}
	}
	return args[0], nil
}
