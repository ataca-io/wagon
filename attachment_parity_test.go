package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAttachmentRetryReusesUploads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	pki := mintClientCert(t)
	var grants, uploads, sends int
	var seen [][]string
	depot := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		if uploads == 2 {
			jsonHandler(http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})(w, r)
			return
		}
		jsonHandler(http.StatusCreated, map[string]string{"id": fmt.Sprintf("file-%d", uploads)})(w, r)
	}))
	t.Cleanup(depot.Close)
	url, ca := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/upload-grants":
			grants++
			jsonHandler(http.StatusCreated, map[string]string{"upload_url": depot.URL + "/u/fixture"})(w, r)
		case "/api/v1/send":
			var req struct{ Attachments []string }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			seen = append(seen, req.Attachments)
			sends++
			if sends == 1 {
				jsonHandler(http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})(w, r)
				return
			}
			jsonHandler(http.StatusOK, map[string]string{"id": "message"})(w, r)
		default:
			http.NotFound(w, r)
		}
	}), pki.clientCAs)
	first := writeAttachFixture(t, "first.txt", "first")
	second := writeAttachFixture(t, "second.txt", "second")
	args := attachSendArgs(url, pki, ca, "--request-id", "same-request", "--attach-file", first, "--attach-file", second)
	for i, want := range []int{4, 4, 0} {
		var stdout, stderr bytes.Buffer
		if got := run(args, &stdout, &stderr); got != want {
			t.Errorf("attempt %d exit = %d, want %d, stderr = %s", i, got, want, stderr.String())
		}
	}
	if grants != 2 || uploads != 3 || sends != 2 {
		t.Errorf("calls = %d grants, %d uploads, %d sends, want 2, 3, 2", grants, uploads, sends)
	}
	for _, ids := range seen {
		if !slices.Equal(ids, []string{"file-1", "file-3"}) {
			t.Errorf("attachments = %v, want [file-1 file-3]", ids)
		}
	}
}

func TestAttachmentInlineAndID(t *testing.T) {
	env := newAttachEnv(t)
	file := writeAttachFixture(t, "example.txt", "hello")
	_, stderr, got := env.run(true, "--attachment-id", "existing-id", "--inline-file", file)
	if got != 0 {
		t.Fatalf("exit = %d, want 0, stderr = %s", got, stderr.String())
	}
	if env.grantCalls != 0 || env.uploadCalls != 0 || env.sendCalls != 1 {
		t.Errorf("calls = %d grants, %d uploads, %d sends, want 0, 0, 1", env.grantCalls, env.uploadCalls, env.sendCalls)
	}
	if got := fmt.Sprint(env.capturedSend["attachments"]); got != "[existing-id]" {
		t.Errorf("attachments = %s, want [existing-id]", got)
	}
	files, _ := env.capturedSend["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files = %v, want one inline file", files)
	}
	fileJSON, _ := files[0].(map[string]any)
	if fileJSON["filename"] != "example.txt" || fileJSON["content"] != "aGVsbG8=" {
		t.Errorf("inline file = %v, want example.txt and the padded base64 for hello", fileJSON)
	}
}

func TestAttachmentGrantBatches(t *testing.T) {
	for _, count := range []int{20, 21} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			env := newAttachEnv(t)
			var args []string
			for i := range count {
				file := writeAttachFixture(t, fmt.Sprintf("f%d.txt", i), "content")
				args = append(args, "--attach-file", file)
			}
			_, stderr, exit := env.run(true, args...)
			if count > 20 {
				if exit != 1 || env.grantCalls != 0 || env.uploadCalls != 0 || env.sendCalls != 0 {
					t.Fatalf("exit = %d, want 1 with no network calls, stderr = %s", exit, stderr.String())
				}
				return
			}
			if exit != 0 || env.grantCalls != 2 || env.uploadCalls != 20 || env.sendCalls != 1 {
				t.Fatalf("exit = %d, calls = %d grants, %d uploads, %d sends; want 0, 2, 20, 1; stderr = %s",
					exit, env.grantCalls, env.uploadCalls, env.sendCalls, stderr.String())
			}
			ids, _ := env.capturedSend["attachments"].([]any)
			for i, id := range ids {
				if id != fmt.Sprintf("f%d.txt", i) {
					t.Errorf("attachments[%d] = %v, want f%d.txt", i, id, i)
				}
			}
			if len(ids) != count {
				t.Errorf("sent %d attachments, want %d", len(ids), count)
			}
		})
	}
}

func TestMixedAttachmentExpiredReceiptRetriesOnlyAfterRejection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	pki := mintClientCert(t)
	var uploads, sends int
	var seen [][]string
	depot := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		jsonHandler(http.StatusCreated, map[string]string{"id": fmt.Sprintf("file-%d", uploads)})(w, r)
	}))
	t.Cleanup(depot.Close)
	url, ca := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/upload-grants":
			jsonHandler(http.StatusCreated, map[string]string{"upload_url": depot.URL + "/u/fixture"})(w, r)
		case "/api/v1/send":
			var req struct{ Attachments []string }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			seen = append(seen, req.Attachments)
			sends++
			switch sends {
			case 1:
				jsonHandler(http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})(w, r)
			case 2:
				jsonHandler(http.StatusUnprocessableEntity, map[string]string{"error": "attachment_unavailable"})(w, r)
			default:
				jsonHandler(http.StatusAccepted, map[string]string{"id": "message"})(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}), pki.clientCAs)
	file := writeAttachFixture(t, "example.txt", "hello")
	inline := writeAttachFixture(t, "inline.txt", "inline content")
	args := attachSendArgs(url, pki, ca, "--request-id", "expired-receipt", "--attach-file", file, "--inline-file", inline)
	var out, stderr bytes.Buffer
	if exit := run(args, &out, &stderr); exit != 4 {
		t.Fatalf("first attempt exit = %d, want 4, stderr = %s", exit, stderr.String())
	}
	dir, err := attachCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache entries = %v (%v), want one", entries, err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, entries[0].Name()), old, old); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if exit := run(args, &out, &stderr); exit != 0 {
		t.Fatalf("second attempt exit = %d, want 0, stderr = %s", exit, stderr.String())
	}
	if uploads != 2 || fmt.Sprint(seen) != "[[file-1] [file-1] [file-2]]" {
		t.Errorf("uploads = %d, sent IDs = %v, want 2 uploads and [[file-1] [file-1] [file-2]]", uploads, seen)
	}
}

func TestAttachmentRejectsRedirectFromRail(t *testing.T) {
	file := writeAttachFixture(t, "example.txt", "attachment content")
	for _, endpoint := range []string{"/api/v1/send", "/api/v1/upload-grants"} {
		t.Run(endpoint, func(t *testing.T) {
			var forwarded atomic.Int32
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				forwarded.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(target.Close)
			pki := mintClientCert(t)
			rail, ca := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != endpoint {
					t.Errorf("rail got path %q, want %q", r.URL.Path, endpoint)
				}
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}), pki.clientCAs)
			fileFlag := "--inline-file"
			if endpoint == "/api/v1/upload-grants" {
				fileFlag = "--attach-file"
			}
			var out, stderr bytes.Buffer
			if got := run(attachSendArgs(rail, pki, ca, fileFlag, file), &out, &stderr); got != 4 {
				t.Errorf("exit = %d, want 4, stderr = %s", got, stderr.String())
			}
			if got := forwarded.Load(); got != 0 {
				t.Errorf("redirect target received %d requests, want none", got)
			}
		})
	}
}

func TestAttachmentRejectsHTTPGrant(t *testing.T) {
	file := writeAttachFixture(t, "example.txt", "attachment content")
	var uploads atomic.Int32
	depot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		uploads.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(depot.Close)
	pki := mintClientCert(t)
	handler := jsonHandler(http.StatusCreated, map[string]string{"upload_url": depot.URL + "/u/fixture"})
	rail, ca := newTLSServer(t, handler, pki.clientCAs)
	var out, stderr bytes.Buffer
	if got := run(attachSendArgs(rail, pki, ca, "--attach-file", file), &out, &stderr); got != 4 {
		t.Errorf("exit = %d, want 4, stderr = %s", got, stderr.String())
	}
	if got := uploads.Load(); got != 0 || !strings.Contains(stderr.String(), "invalid HTTPS grant upload URL") {
		t.Errorf("HTTP requests = %d, stderr = %q, want zero requests and the HTTPS error", got, stderr.String())
	}
}
