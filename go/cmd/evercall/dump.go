package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// requestSeq numbers the dumps so interleaved deliveries can be told apart.
var requestSeq atomic.Uint64

// dumpHandler accepts a callback on any HTTP method and writes the whole
// request — request line, headers and body — to stdout, then answers 200.
//
// It always answers 200, even on a body it cannot read: the provider retries
// non-2xx, and a retry loop would bury the one delivery we are trying to
// inspect. That is the opposite of the rule the real ingest handler must follow
// (2xx only after a durable publish), and it is only acceptable because nothing
// here is published yet.
func (app *application) dumpHandler(w http.ResponseWriter, r *http.Request) {
	n := requestSeq.Add(1)

	body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, app.config.maxBodyBytes))

	var buf bytes.Buffer

	fmt.Fprintf(&buf, "\n=== #%d %s %s\n", n, time.Now().UTC().Format(time.RFC3339Nano), strings.Repeat("=", 40))
	fmt.Fprintf(&buf, "%s %s %s\n", r.Method, r.URL.RequestURI(), r.Proto)
	fmt.Fprintf(&buf, "Host: %s\n", r.Host)
	fmt.Fprintf(&buf, "RemoteAddr: %s\n", r.RemoteAddr)
	if r.TLS != nil {
		fmt.Fprintf(&buf, "TLS: %s\n", r.TLS.ServerName)
	}

	writeQuery(&buf, r.URL.Query())
	writeHeaders(&buf, r.Header)
	writeBody(&buf, r, body, readErr)

	buf.WriteString("=== end\n")

	// One Write per request so concurrent deliveries don't interleave.
	app.logger.Print(buf.String())

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "{\"status\":\"received\",\"seq\":%d}\n", n)
}

func writeQuery(buf *bytes.Buffer, q url.Values) {
	if len(q) == 0 {
		return
	}

	buf.WriteString("--- query\n")
	writeValues(buf, q)
}

func writeHeaders(buf *bytes.Buffer, h http.Header) {
	buf.WriteString("--- headers\n")
	if len(h) == 0 {
		buf.WriteString("(none)\n")
		return
	}
	writeValues(buf, url.Values(h))
}

// writeValues prints a multi-map with sorted keys, so two dumps of the same
// request are diffable.
func writeValues(buf *bytes.Buffer, vals url.Values) {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		for _, v := range vals[k] {
			fmt.Fprintf(buf, "%s: %s\n", k, v)
		}
	}
}

func writeBody(buf *bytes.Buffer, r *http.Request, body []byte, readErr error) {
	fmt.Fprintf(buf, "--- body (%d bytes read", len(body))
	if r.ContentLength >= 0 {
		fmt.Fprintf(buf, ", Content-Length %d", r.ContentLength)
	}
	buf.WriteString(")\n")

	if readErr != nil {
		fmt.Fprintf(buf, "!! read error: %v\n", readErr)
	}

	if len(body) == 0 {
		buf.WriteString("(empty)\n")
		return
	}

	// The raw bytes are the evidence, so they are always printed verbatim.
	buf.Write(body)
	if !bytes.HasSuffix(body, []byte("\n")) {
		buf.WriteByte('\n')
	}

	// Decoded views are additions, not replacements — they make the provider's
	// field names readable while discovering the schema.
	switch {
	case json.Valid(body):
		var indented bytes.Buffer
		if err := json.Indent(&indented, body, "", "  "); err == nil {
			buf.WriteString("--- body (json)\n")
			buf.Write(indented.Bytes())
			buf.WriteByte('\n')
		}
	case isFormEncoded(r):
		if form, err := url.ParseQuery(string(body)); err == nil {
			buf.WriteString("--- body (form)\n")
			writeValues(buf, form)
		}
	}
}

func isFormEncoded(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")

	return strings.HasPrefix(ct, "application/x-www-form-urlencoded")
}
