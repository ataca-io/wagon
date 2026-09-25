package main

import (
	"bytes"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// complete runs `wagon __complete words...` and returns its output lines.
func complete(t *testing.T, words ...string) []string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if got := run(append([]string{"__complete"}, words...), &stdout, &stderr); got != 0 {
		t.Fatalf("run(__complete %q) exit = %d, want 0; stderr = %s", words, got, stderr.String())
	}
	return strings.Fields(stdout.String())
}

func TestComplete(t *testing.T) {
	// An empty HOME and RAIL_CERT keep a real certificate out of --from.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RAIL_CERT", "")

	tests := []struct {
		name  string
		words []string
		want  []string
	}{
		{"no words", nil, []string{"auth", "completion", "forms", "message", "messages", "send", "send-file", "test", "version", "whoami"}},
		{"partial verb", []string{"s"}, []string{"send", "send-file"}},
		{"messages subcommands", []string{"messages", ""}, []string{"deliveries", "list", "opens"}},
		{"forms subcommands after --json", []string{"--json", "forms", ""}, []string{"create", "delete", "get", "list", "submissions", "update"}},
		{"auth subcommands", []string{"auth", ""}, []string{"setup", "show"}},
		{"completion shells", []string{"completion", ""}, []string{"bash", "fish", "zsh"}},
		{"global flags", []string{"--"}, []string{"--ca", "--cert", "--json", "--key", "--server"}},
		{"global flag values are skipped", []string{"--server", "https://x:443", "--ca", "ca.crt", "te"}, []string{"test"}},
		{"verb flags", []string{"send", "--subject", "hi", "--t"}, []string{"--text", "--to"}},
		{"flags after a form token", []string{"forms", "submissions", "tok", "--"}, []string{"--before", "--id", "--limit"}},
		{"status values", []string{"messages", "list", "--status", "d"}, []string{"delivered"}},
		{"file flag value", []string{"send", "--attach-file", ""}, []string{completeFile}},
		{"global file flag value", []string{"--cert", ""}, []string{completeFile}},
		{"send-file arguments", []string{"send-file", "--to", "a@b.c", ""}, []string{completeFile}},
		{"auth setup arguments", []string{"auth", "setup", ""}, []string{completeFile}},
		{"free-form flag value", []string{"send", "--subject", ""}, nil},
		{"message id", []string{"messages", "opens", ""}, nil},
		{"unknown verb", []string{"bogus", ""}, nil},
		{"no certificate for --from", []string{"send", "--from", ""}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := complete(t, tt.words...); !slices.Equal(got, tt.want) {
				t.Errorf("__complete %q = %q, want %q", tt.words, got, tt.want)
			}
		})
	}
}

func TestCompleteFromSenders(t *testing.T) {
	pki := mintClientCertWithSenders(t, "noreply@example.com", "hello@example.com")
	want := []string{"noreply@example.com", "hello@example.com"}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".wagon"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pki.certFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".wagon", authCert), data, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		home, env string
		words     []string
		want      []string
	}{
		{"--cert word", t.TempDir(), "", []string{"--cert", pki.certFile, "send", "--from", ""}, want},
		{"--cert=path word", t.TempDir(), "", []string{"--cert=" + pki.certFile, "send", "--from", ""}, want},
		{"RAIL_CERT", t.TempDir(), pki.certFile, []string{"send", "--from", ""}, want},
		{"~/.wagon", home, "", []string{"send-file", "--from", ""}, want},
		{"prefix filter", home, "", []string{"forms", "create", "--from", "h"}, want[1:]},
		{"missing certificate", t.TempDir(), "", []string{"--cert", "/does/not/exist", "send", "--from", ""}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", tt.home)
			t.Setenv("RAIL_CERT", tt.env)
			if got := complete(t, tt.words...); !slices.Equal(got, tt.want) {
				t.Errorf("__complete %q = %q, want %q", tt.words, got, tt.want)
			}
		})
	}
}

// flagLine matches one flag in flag.PrintDefaults output: "  -name" for a
// boolean, "  -name type" for a flag that takes a value.
var flagLine = regexp.MustCompile(`(?m)^  -([\w-]+)( \S+)?$`)

// TestCompletionFlagsMatch guards completionTree against drift: each verb's
// real FlagSet, printed by -h, must match the table. -h returns before any
// verb touches its client, so a nil client is safe.
func TestCompletionFlagsMatch(t *testing.T) {
	type runFunc func(stderr io.Writer) error
	verbs := map[string]runFunc{
		"whoami":            func(w io.Writer) error { return runWhoami(nil, []string{"-h"}, io.Discard, w) },
		"send":              func(w io.Writer) error { return runSend(nil, []string{"-h"}, io.Discard, w) },
		"send-file":         func(w io.Writer) error { return runSendFile(nil, []string{"-h"}, io.Discard, w) },
		"test":              func(w io.Writer) error { return runTest(nil, []string{"-h"}, io.Discard, w) },
		"messages list":     func(w io.Writer) error { return runMessagesList(nil, []string{"-h"}, io.Discard, w) },
		"forms list":        func(w io.Writer) error { return runFormsList(nil, []string{"-h"}, io.Discard, w) },
		"forms create":      func(w io.Writer) error { return runFormsCreate(nil, []string{"-h"}, io.Discard, w) },
		"forms update":      func(w io.Writer) error { return runFormsUpdate(nil, []string{"tok", "-h"}, io.Discard, w) },
		"forms submissions": func(w io.Writer) error { return runFormsSubmissions(nil, []string{"tok", "-h"}, io.Discard, w) },
	}
	for path, fn := range verbs {
		t.Run(path, func(t *testing.T) {
			var help bytes.Buffer
			_ = fn(&help)
			got := map[string]bool{}
			for _, m := range flagLine.FindAllStringSubmatch(help.String(), -1) {
				got[m[1]] = m[2] != ""
			}

			spec := completionTree
			for word := range strings.FieldsSeq(path) {
				spec = spec.subs[word]
			}
			if !maps.Equal(got, spec.flags) {
				t.Errorf("FlagSet flags = %v, completion table = %v", got, spec.flags)
			}
		})
	}
}

// TestCompletionCommandsMatch checks the table's verbs and subcommands
// against the lists wagon itself prints.
func TestCompletionCommandsMatch(t *testing.T) {
	var stderr bytes.Buffer
	run(nil, io.Discard, &stderr)
	verbs := regexp.MustCompile(`Verbs: (.*)`).FindStringSubmatch(stderr.String())
	if verbs == nil {
		t.Fatalf("usage has no Verbs line: %s", stderr.String())
	}
	wantVerbs := strings.Split(verbs[1], ", ")
	slices.Sort(wantVerbs)
	if got := slices.Sorted(maps.Keys(completionTree.subs)); !slices.Equal(got, wantVerbs) {
		t.Errorf("completion verbs = %q, usage verbs = %q", got, wantVerbs)
	}

	// The subcommand usage error lists the subcommands as <a|b|c>.
	subs := map[string]error{
		"messages": runMessages(nil, nil, io.Discard, io.Discard),
		"forms":    runForms(nil, nil, io.Discard, io.Discard),
	}
	for verb, err := range subs {
		m := regexp.MustCompile(`<([\w|-]+)>`).FindStringSubmatch(err.Error())
		if m == nil {
			t.Fatalf("%s usage error lists no subcommands: %v", verb, err)
		}
		want := strings.Split(m[1], "|")
		slices.Sort(want)
		if got := slices.Sorted(maps.Keys(completionTree.subs[verb].subs)); !slices.Equal(got, want) {
			t.Errorf("%s completion subcommands = %q, usage = %q", verb, got, want)
		}
	}

	// Global flags come from the top-level usage line, [--name] or [--name VALUE].
	got := map[string]bool{}
	for _, m := range regexp.MustCompile(`\[--([\w-]+)( [A-Z]+)?\]`).FindAllStringSubmatch(stderr.String(), -1) {
		got[m[1]] = m[2] != ""
	}
	if !maps.Equal(got, completionTree.flags) {
		t.Errorf("usage global flags = %v, completion table = %v", got, completionTree.flags)
	}
}

func TestRunCompletion(t *testing.T) {
	// Completion must work before `wagon auth setup`: no HOME certificate.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RAIL_CERT", "")
	t.Setenv("RAIL_KEY", "")

	checkers := map[string][]string{
		"bash": {"bash", "-n"},
		"zsh":  {"zsh", "-n"},
		"fish": {"fish", "--no-execute"},
	}
	for shell, checker := range checkers {
		t.Run(shell, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run([]string{"completion", shell}, &stdout, &stderr); got != 0 {
				t.Fatalf("run(completion %s) exit = %d, want 0; stderr = %s", shell, got, stderr.String())
			}
			if !strings.Contains(stdout.String(), "wagon __complete") {
				t.Errorf("completion %s script does not call wagon __complete", shell)
			}

			if _, err := exec.LookPath(checker[0]); err != nil {
				t.Skipf("%s not installed; syntax not checked", checker[0])
			}
			script := filepath.Join(t.TempDir(), "wagon."+shell)
			if err := os.WriteFile(script, stdout.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.CommandContext(t.Context(), checker[0], append(checker[1:], script)...).CombinedOutput(); err != nil {
				t.Errorf("%s syntax check: %v\n%s", shell, err, out)
			}
		})
	}

	for _, args := range [][]string{{"completion"}, {"completion", "ksh"}} {
		var stdout, stderr bytes.Buffer
		if got := run(args, &stdout, &stderr); got != 1 {
			t.Errorf("run(%q) exit = %d, want 1", args, got)
		}
	}
}
