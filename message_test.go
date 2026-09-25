package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// messageMux serves the three endpoints `wagon message` calls for message
// 01msg, and counts the calls per path.
func messageMux(calls map[string]int) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.PathValue("id") != "01msg" {
			jsonHandler(http.StatusNotFound, map[string]any{"error": "not_found", "message": "message not found"})(w, r)
			return
		}
		jsonHandler(http.StatusOK, map[string]any{
			"id": "01msg", "request_id": "req-1", "status": "delivered", "direction": "outbound",
			"from": "noreply@example.com", "to": []string{"a@x.com", "b@y.com"}, "size": 1234,
			"created_at": "2026-09-25T06:00:00Z", "updated_at": "2026-09-25T06:00:05Z",
			// One file an API send carried, and one inbound part depot found infected.
			"attachments": []map[string]any{
				{"filename": "report.pdf", "content_type": "application/pdf", "size": 2048},
				{"filename": "invoice.exe", "content_type": "application/octet-stream", "size": 999, "verdict": "infected"},
			},
		})(w, r)
	})
	mux.HandleFunc("GET /api/v1/messages/{id}/deliveries", func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		jsonHandler(http.StatusOK, map[string]any{
			"message_id": "01msg", "status": "delivered",
			"deliveries": []map[string]any{{"recipient": "a@x.com", "domain": "x.com", "status": "delivered", "attempts": 1, "response_code": 250, "response": "ok"}},
		})(w, r)
	})
	mux.HandleFunc("GET /api/v1/messages/{id}/opens", func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		jsonHandler(http.StatusOK, map[string]any{
			"message_id": "01msg",
			"opens":      []map[string]any{{"opened_at": "2026-09-25T07:00:00Z", "user_agent": "TestMail/1.0"}},
		})(w, r)
	})
	return mux
}

func TestRunMessage(t *testing.T) {
	pki := mintClientCert(t)
	calls := map[string]int{}
	url, caFile := newTLSServer(t, messageMux(calls), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	if got := run(baseArgs(url, pki, caFile, "message", "01msg"), &stdout, &stderr); got != 0 {
		t.Fatalf("run(message) exit = %d, want 0; stderr = %s", got, stderr.String())
	}
	for _, want := range []string{
		"id:", "01msg", "a@x.com, b@y.com", "updated_at:", "2026-09-25T06:00:05Z", "first_opened_at:",
		"files:", "FILENAME", "report.pdf", "application/pdf", "2048", "invoice.exe", "infected",
		"deliveries:", "RECIPIENT", "250 ok",
		"opens:", "OPENED_AT", "TestMail/1.0",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}
	if !regexp.MustCompile(`(?m)^attachments:\s+2$`).MatchString(stdout.String()) {
		t.Errorf("stdout lacks the line \"attachments: 2\":\n%s", stdout.String())
	}
	for _, path := range []string{"/api/v1/messages/01msg", "/api/v1/messages/01msg/deliveries", "/api/v1/messages/01msg/opens"} {
		if calls[path] != 1 {
			t.Errorf("calls to %s = %d, want 1", path, calls[path])
		}
	}
}

func TestRunMessageJSON(t *testing.T) {
	pki := mintClientCert(t)
	url, caFile := newTLSServer(t, messageMux(map[string]int{}), pki.clientCAs)

	var stdout, stderr bytes.Buffer
	if got := run(append([]string{"--json"}, baseArgs(url, pki, caFile, "message", "01msg")...), &stdout, &stderr); got != 0 {
		t.Fatalf("run(--json message) exit = %d, want 0; stderr = %s", got, stderr.String())
	}
	var report struct {
		Message    map[string]any `json:"message"`
		Deliveries map[string]any `json:"deliveries"`
		Opens      map[string]any `json:"opens"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("--json stdout is not valid JSON: %v\n%s", err, stdout.String())
	}
	if report.Message["updated_at"] != "2026-09-25T06:00:05Z" {
		t.Errorf("message = %v, want the GET /api/v1/messages/{id} body", report.Message)
	}
	if d, _ := report.Deliveries["deliveries"].([]any); len(d) != 1 {
		t.Errorf("deliveries = %v, want the deliveries response with 1 entry", report.Deliveries)
	}
	if o, _ := report.Opens["opens"].([]any); len(o) != 1 {
		t.Errorf("opens = %v, want the opens response with 1 entry", report.Opens)
	}
}

func TestRunMessageErrors(t *testing.T) {
	pki := mintClientCert(t)
	tests := []struct {
		name string
		args []string
		exit int
	}{
		{"unknown id", []string{"message", "nope"}, 3},
		{"no id", []string{"message"}, 1},
		{"two ids", []string{"message", "a", "b"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := map[string]int{}
			url, caFile := newTLSServer(t, messageMux(calls), pki.clientCAs)
			var stdout, stderr bytes.Buffer
			if got := run(baseArgs(url, pki, caFile, tt.args...), &stdout, &stderr); got != tt.exit {
				t.Fatalf("run(%q) exit = %d, want %d; stderr = %s", tt.args, got, tt.exit, stderr.String())
			}
			// Deliveries and opens are never fetched once the message itself fails.
			for path, n := range calls {
				if strings.HasSuffix(path, "/deliveries") || strings.HasSuffix(path, "/opens") {
					t.Errorf("calls to %s = %d, want 0", path, n)
				}
			}
		})
	}
}
