package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestApp(out *bytes.Buffer) *application {
	return &application{
		config: config{maxBodyBytes: 1 << 20},
		logger: log.New(out, "", 0),
	}
}

func TestDumpHandlerLogsAnyMethod(t *testing.T) {
	methods := []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions, "WEIRD",
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			var out bytes.Buffer
			app := newTestApp(&out)

			req := httptest.NewRequest(method, "/callback/anything?a=1", strings.NewReader("hello"))
			req.Header.Set("X-Evercall-Signature", "sig-123")
			rec := httptest.NewRecorder()

			app.routes().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}

			logged := out.String()
			for _, want := range []string{method, "/callback/anything?a=1", "X-Evercall-Signature: sig-123", "a: 1", "hello"} {
				if !strings.Contains(logged, want) {
					t.Errorf("log missing %q\n--- log ---\n%s", want, logged)
				}
			}
		})
	}
}

func TestDumpHandlerDecodesBodies(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		want        []string
	}{
		{
			name:        "json is indented as well as raw",
			contentType: "application/json",
			body:        `{"callId":"abc","from":"+4512345678"}`,
			want:        []string{`{"callId":"abc","from":"+4512345678"}`, "--- body (json)", `"callId": "abc"`},
		},
		{
			name:        "form is expanded",
			contentType: "application/x-www-form-urlencoded",
			body:        "callId=abc&from=%2B4512345678",
			want:        []string{"--- body (form)", "callId: abc", "from: +4512345678"},
		},
		{
			name:        "unknown content type is still dumped raw",
			contentType: "application/octet-stream",
			body:        "not-parseable",
			want:        []string{"not-parseable"},
		},
		{
			name:        "empty body is reported",
			contentType: "application/json",
			body:        "",
			want:        []string{"(empty)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			app := newTestApp(&out)

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()

			app.routes().ServeHTTP(rec, req)

			logged := out.String()
			for _, want := range tt.want {
				if !strings.Contains(logged, want) {
					t.Errorf("log missing %q\n--- log ---\n%s", want, logged)
				}
			}
		})
	}
}

// An oversized body must not turn into a retry loop: we log what we got and
// still answer 200.
func TestDumpHandlerAnswersOKOnOversizedBody(t *testing.T) {
	var out bytes.Buffer
	app := newTestApp(&out)
	app.config.maxBodyBytes = 8

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 100)))
	rec := httptest.NewRecorder()

	app.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(out.String(), "read error") {
		t.Errorf("log missing read error\n--- log ---\n%s", out.String())
	}
}

func TestHealthcheckIsNotDumped(t *testing.T) {
	var out bytes.Buffer
	app := newTestApp(&out)

	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	rec := httptest.NewRecorder()

	app.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if out.Len() != 0 {
		t.Errorf("healthcheck was dumped: %s", out.String())
	}
}
