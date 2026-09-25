package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// printPretty writes raw as indented JSON, wagon's --json escape hatch for
// every verb.
func printPretty(out io.Writer, raw []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		_, err := fmt.Fprintln(out, string(raw))
		return err
	}
	buf.WriteByte('\n')
	_, err := out.Write(buf.Bytes())
	return err
}

// renderKV prints a key: value block, one pair per line, keys aligned.
func renderKV(out io.Writer, pairs [][2]string) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, p := range pairs {
		_, _ = fmt.Fprintf(tw, "%s:\t%s\n", p[0], p[1])
	}
	_ = tw.Flush()
}

// renderTable prints rows as an aligned table under headers.
func renderTable(out io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	_ = tw.Flush()
}
