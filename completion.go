package main

import (
	"cmp"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

//go:embed completion/wagon.bash
var bashCompletion string

//go:embed completion/_wagon
var zshCompletion string

//go:embed completion/wagon.fish
var fishCompletion string

// completeFile is the __complete output that asks the shell for its own
// file-path completion.
const completeFile = ":file"

// completionSpec is one command in the completion tree. flags maps each flag
// name to whether it takes a value; files marks file-path arguments.
type completionSpec struct {
	flags map[string]bool
	subs  map[string]completionSpec
	files bool
}

// completionFormFlags are registerFormFlags' flags, shared by forms create and update.
var completionFormFlags = map[string]bool{
	"name": true, "subject": true, "redirect": true, "rate-limit": true, "cc-submitter": false,
	"upload-max-files": true, "upload-max-bytes": true, "upload-ttl": true,
	"to": true, "origin": true, "upload-type": true, "label": true,
}

// completionTree mirrors run's global flags and dispatch. TestCompletionFlagsMatch
// fails when a verb's FlagSet and this table disagree.
var completionTree = completionSpec{
	flags: map[string]bool{"server": true, "cert": true, "key": true, "ca": true, "json": false},
	subs: map[string]completionSpec{
		"whoami": {},
		"send": {flags: map[string]bool{
			"from": true, "to": true, "cc": true, "subject": true, "text": true, "html": true,
			"body-file": true, "reply-to": true, "unsubscribe-url": true, "request-id": true, "attach-file": true,
		}},
		"send-file": {files: true, flags: map[string]bool{
			"from": true, "to": true, "cc": true, "subject": true, "text": true, "request-id": true,
		}},
		"messages": {subs: map[string]completionSpec{
			"list": {flags: map[string]bool{
				"status": true, "from": true, "to": true, "since": true, "until": true, "limit": true, "before": true,
			}},
			"deliveries": {},
			"opens":      {},
		}},
		"forms": {subs: map[string]completionSpec{
			"list":        {},
			"get":         {},
			"create":      {flags: withFlags(completionFormFlags, map[string]bool{"from": true})},
			"update":      {flags: withFlags(completionFormFlags, map[string]bool{"enabled": false})},
			"delete":      {},
			"submissions": {flags: map[string]bool{"id": true, "before": true, "limit": true}},
		}},
		"test": {flags: map[string]bool{"to": true}},
		"auth": {subs: map[string]completionSpec{
			"setup": {files: true},
			"show":  {},
		}},
		"version":    {},
		"completion": {subs: map[string]completionSpec{"bash": {}, "zsh": {}, "fish": {}}},
	},
}

func withFlags(base, extra map[string]bool) map[string]bool {
	out := maps.Clone(base)
	maps.Copy(out, extra)
	return out
}

// fileFlags take a file path as their value.
var fileFlags = map[string]bool{"cert": true, "key": true, "ca": true, "body-file": true, "attach-file": true}

// statusValues are messages list --status's accepted values.
var statusValues = []string{"pending", "partial", "delivered", "bounced", "unroutable"}

// runCompletion implements `wagon completion <bash|zsh|fish>`.
func runCompletion(args []string, out io.Writer) error {
	if len(args) != 1 {
		return &usageError{"usage: wagon completion <bash|zsh|fish>"}
	}
	scripts := map[string]string{"bash": bashCompletion, "zsh": zshCompletion, "fish": fishCompletion}
	script, ok := scripts[args[0]]
	if !ok {
		return &usageError{fmt.Sprintf("unsupported shell %q; want bash, zsh, or fish", args[0])}
	}
	_, err := io.WriteString(out, script)
	return err
}

// runComplete implements the hidden `wagon __complete <words...>`. words are
// the command line after "wagon", ending with the word under the cursor. It
// prints matching candidates one per line, and never calls rail.
func runComplete(words []string, out io.Writer) {
	if len(words) == 0 {
		words = []string{""}
	}
	cur, done := words[len(words)-1], words[:len(words)-1]

	spec := completionTree
	var certPath, pendingFlag string
	for _, w := range done {
		if pendingFlag != "" {
			if pendingFlag == "cert" {
				certPath = w
			}
			pendingFlag = ""
			continue
		}
		if name, value, inline, isFlag := splitFlag(w); isFlag {
			if inline && name == "cert" {
				certPath = value
			}
			if !inline && spec.flags[name] {
				pendingFlag = name
			}
			continue
		}
		if len(spec.subs) > 0 {
			sub, ok := spec.subs[w]
			if !ok {
				return
			}
			spec = sub
		}
	}

	var candidates []string
	switch {
	case pendingFlag == "status":
		candidates = statusValues
	case pendingFlag == "from":
		candidates = certSenders(certPath)
	case fileFlags[pendingFlag]:
		_, _ = fmt.Fprintln(out, completeFile)
		return
	case pendingFlag != "":
		return
	case strings.HasPrefix(cur, "-"):
		for name := range spec.flags {
			candidates = append(candidates, "--"+name)
		}
		slices.Sort(candidates)
	case len(spec.subs) > 0:
		candidates = slices.Sorted(maps.Keys(spec.subs))
	case spec.files:
		_, _ = fmt.Fprintln(out, completeFile)
		return
	}

	for _, c := range candidates {
		if strings.HasPrefix(c, cur) {
			_, _ = fmt.Fprintln(out, c)
		}
	}
}

// splitFlag splits "-name", "--name", or "--name=value"; inline reports an
// "=value" part.
func splitFlag(w string) (name, value string, inline, ok bool) {
	if len(w) < 2 || w[0] != '-' {
		return "", "", false, false
	}
	name, value, inline = strings.Cut(strings.TrimPrefix(w[1:], "-"), "=")
	return name, value, inline, true
}

// certSenders returns the email SANs of the certificate named by --cert,
// RAIL_CERT, or ~/.wagon, in that order. It reads no key, and any failure
// yields no senders.
func certSenders(certPath string) []string {
	path := cmp.Or(certPath, os.Getenv("RAIL_CERT"), filepath.Join(wagonDir(), authCert))
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path = filepath.Join(home, rest)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return cert.EmailAddresses
}
