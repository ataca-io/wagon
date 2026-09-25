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
// Its attachments list shadows messageSummary's count of the same name.
type messageDetail struct {
	messageSummary
	UpdatedAt   string              `json:"updated_at"`
	Attachments []messageAttachment `json:"attachments"`
}

// messageAttachment is one file a message carried. Verdict is set for an
// inbound part, which depot scanned.
type messageAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Verdict     string `json:"verdict,omitempty"`
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
		{"attachments", fmt.Sprintf("%d", len(msg.Attachments))},
		{"created_at", msg.CreatedAt},
		{"updated_at", msg.UpdatedAt},
		{"first_opened_at", orDash(msg.FirstOpenedAt)},
	})
	_, _ = fmt.Fprintln(out, "\nattachments:")
	renderAttachments(out, msg.Attachments)
	_, _ = fmt.Fprintln(out, "\ndeliveries:")
	renderDeliveries(out, deliveries.Deliveries)
	_, _ = fmt.Fprintln(out, "\nopens:")
	renderOpens(out, opens.Opens)
	return nil
}

func renderAttachments(out io.Writer, attachments []messageAttachment) {
	rows := make([][]string, 0, len(attachments))
	for _, a := range attachments {
		rows = append(rows, []string{a.Filename, orDash(a.ContentType), fmt.Sprintf("%d", a.Size), orDash(a.Verdict)})
	}
	renderTable(out, []string{"FILENAME", "CONTENT_TYPE", "SIZE", "VERDICT"}, rows)
}
