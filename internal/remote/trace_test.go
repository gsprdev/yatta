package remote

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"jql":"project = X"}` {
			t.Errorf("server received %q; the trace must pass the body on", b)
		}
		io.WriteString(w, `{"issues":[]}`)
	}))
	defer srv.Close()

	var log strings.Builder
	c := Client{
		BaseURL: srv.URL,
		HTTP:    &http.Client{Transport: Trace(&log, http.DefaultTransport)},
		Auth:    func(r *http.Request) { r.SetBasicAuth("me@example.com", "s3cret") },
	}
	var out struct{ Issues []any }
	if err := c.Do(context.Background(), http.MethodPost, "/search", map[string]string{"jql": "project = X"}, &out); err != nil {
		t.Fatalf("Do: %v; the trace must pass the response on", err)
	}
	got := log.String()
	for _, want := range []string{"POST " + srv.URL + "/search", "(auth: Basic)", `{"jql":"project = X"}`, "200 OK", `{"issues":[]}`} {
		if !strings.Contains(got, want) {
			t.Errorf("trace lacks %q:\n%s", want, got)
		}
	}
	for _, secret := range []string{"s3cret", "me@example.com", "bWVAZXhhbXBsZS5jb206czNjcmV0"} {
		if strings.Contains(got, secret) {
			t.Errorf("trace contains the credential %q:\n%s", secret, got)
		}
	}
}
