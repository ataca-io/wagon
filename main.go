// Command wagon is a thin CLI for clients holding a rail client certificate:
// it wraps /who and /api/v1/* over that mTLS connection.
package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ataca-io/wagon/internal/buildinfo"
)

// defaultServer is the public rail deployment, used when neither --server nor
// RAIL_SERVER is set.
const defaultServer = "https://smtp.ataca.io"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses the global flags, dispatches to the verb, and maps the result
// to a process exit code. It never calls os.Exit itself, so tests can drive
// it directly.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wagon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr) }
	server := fs.String("server", os.Getenv("RAIL_SERVER"), "rail server URL (env RAIL_SERVER, default "+defaultServer+")")
	certFile := fs.String("cert", os.Getenv("RAIL_CERT"), "client certificate file (env RAIL_CERT, default ~/.wagon/"+authCert+")")
	keyFile := fs.String("key", os.Getenv("RAIL_KEY"), "client key file (env RAIL_KEY, default ~/.wagon/"+authKey+")")
	caFile := fs.String("ca", os.Getenv("RAIL_CA"), "CA certificate file (env RAIL_CA, default: system pool)")
	jsonOut := fs.Bool("json", false, "print raw JSON responses")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}

	rest := fs.Args()
	if len(rest) < 1 {
		usage(stderr)
		return 1
	}
	verb, verbArgs := rest[0], rest[1:]

	// Both verbs run without a server or a certificate.
	switch verb {
	case "version":
		_, _ = fmt.Fprintln(stdout, buildinfo.String("wagon"))
		return 0
	case "auth":
		if err := runAuth(verbArgs, stdout); err != nil {
			_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		return 0
	}

	cert, key, err := resolveKeys(*certFile, *keyFile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	c, err := newClient(cmp.Or(*server, defaultServer), cert, key, *caFile, *jsonOut)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if err := dispatch(c, verb, verbArgs, stdout, stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return exitCode(err)
	}
	return 0
}

// dispatch runs one verb against an already-configured client.
func dispatch(c *client, verb string, args []string, stdout, stderr io.Writer) error {
	switch verb {
	case "whoami":
		return runWhoami(c, args, stdout, stderr)
	case "send":
		return runSend(c, args, stdout, stderr)
	case "send-file":
		return runSendFile(c, args, stdout, stderr)
	case "messages":
		return runMessages(c, args, stdout, stderr)
	case "forms":
		return runForms(c, args, stdout, stderr)
	case "test":
		return runTest(c, args, stdout, stderr)
	default:
		return &usageError{fmt.Sprintf("unknown verb %q", verb)}
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: wagon [--server URL] [--cert FILE] [--key FILE] [--ca FILE] [--json] <verb> [args]")
	_, _ = fmt.Fprintln(w, "Verbs: whoami, send, send-file, messages, forms, test, auth, version")
	_, _ = fmt.Fprintln(w, "Env fallbacks: RAIL_SERVER, RAIL_CERT, RAIL_KEY, RAIL_CA")
	_, _ = fmt.Fprintln(w, "Defaults: --server "+defaultServer+", --cert and --key from ~/.wagon (see `wagon auth setup`)")
}

// repeatedFlag collects a repeatable string flag, for example --to a@x.com --to b@x.com.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return "" }

func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// visitedFlags returns the set of flag names that Parse actually saw on the
// command line, for a PATCH-style update where only named flags are sent.
func visitedFlags(fs *flag.FlagSet) map[string]bool {
	seen := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	return seen
}

// orDash renders an empty string as "-" for a human key/value block.
func orDash(s string) string {
	return cmp.Or(s, "-")
}
