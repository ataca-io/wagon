package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// messageDetail mirrors httpd's messageDetail (GET /api/v1/messages/{id}).
type messageDetail struct {
	messageSummary
	UpdatedAt string `json:"updated_at"`
}

// messageReport is `wagon --json message`: each endpoint's response, verbatim.
type messageReport struct {
	Message    json.RawMessage `json:"message"`
	Deliveries json.RawMessage `json:"deliveries"`
	Opens      json.RawMessage `json:"opens"`
}

// runMessage implements `wagon message <id>`: the message, its deliveries,
// and its opens in one report.
func runMessage(c *client, args []string, out io.Writer) error {
	id, err := messageIDArg(args)
	if err != nil {
		return err
	}
	path := "/api/v1/messages/" + url.PathEscape(id)

	var msg messageDetail
	rawMsg, err := c.do(http.MethodGet, path, nil, &msg)
	if err != nil {
		return err
	}
	var deliveries deliveriesResponse
	rawDeliveries, err := c.do(http.MethodGet, path+"/deliveries", nil, &deliveries)
	if err != nil {
		return err
	}
	var opens opensResponse
	rawOpens, err := c.do(http.MethodGet, path+"/opens", nil, &opens)
	if err != nil {
		return err
	}

	if c.jsonOut {
		raw, err := json.Marshal(messageReport{Message: rawMsg, Deliveries: rawDeliveries, Opens: rawOpens})
		if err != nil {
			return fmt.Errorf("encode report: %w", err)
		}
		return printPretty(out, raw)
	}
	renderKV(out, [][2]string{
		{"id", msg.ID},
		{"request_id", orDash(msg.RequestID)},
		{"status", msg.Status},
		{"direction", msg.Direction},
		{"from", msg.From},
		{"to", strings.Join(msg.To, ", ")},
		{"size", fmt.Sprintf("%d", msg.Size)},
		{"created_at", msg.CreatedAt},
		{"updated_at", msg.UpdatedAt},
		{"first_opened_at", orDash(msg.FirstOpenedAt)},
	})
	_, _ = fmt.Fprintln(out, "\ndeliveries:")
	renderDeliveries(out, deliveries.Deliveries)
	_, _ = fmt.Fprintln(out, "\nopens:")
	renderOpens(out, opens.Opens)
	return nil
}
