package main

import (
	"bytes"
	"testing"
)

func TestRenderKV(t *testing.T) {
	tests := []struct {
		name  string
		pairs [][2]string
		want  string
	}{
		{
			name:  "aligns keys by the longest one",
			pairs: [][2]string{{"a", "1"}, {"bb", "22"}},
			want:  "a:   1\nbb:  22\n",
		},
		{
			name:  "empty input prints nothing",
			pairs: nil,
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			renderKV(&buf, tt.pairs)
			if got := buf.String(); got != tt.want {
				t.Errorf("renderKV(%v) = %q, want %q", tt.pairs, got, tt.want)
			}
		})
	}
}

func TestRenderTable(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		rows    [][]string
		want    string
	}{
		{
			name:    "aligns columns by the widest cell",
			headers: []string{"HEAD1", "HEAD2"},
			rows:    [][]string{{"x", "yy"}, {"xxxx", "y"}},
			want:    "HEAD1  HEAD2\nx      yy\nxxxx   y\n",
		},
		{
			name:    "header only when there are no rows",
			headers: []string{"TOKEN", "NAME"},
			rows:    nil,
			want:    "TOKEN  NAME\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			renderTable(&buf, tt.headers, tt.rows)
			if got := buf.String(); got != tt.want {
				t.Errorf("renderTable(%v, %v) = %q, want %q", tt.headers, tt.rows, got, tt.want)
			}
		})
	}
}

func TestPrintPretty(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "indents a JSON object",
			raw:  []byte(`{"a":1,"b":"x"}`),
			want: "{\n  \"a\": 1,\n  \"b\": \"x\"\n}\n",
		},
		{
			name: "falls back to printing raw bytes when not JSON",
			raw:  []byte("not-json"),
			want: "not-json\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := printPretty(&buf, tt.raw); err != nil {
				t.Fatalf("printPretty(%q) error = %v", tt.raw, err)
			}
			if got := buf.String(); got != tt.want {
				t.Errorf("printPretty(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
