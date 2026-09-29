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
		Auth:    func(r *http.Request) { r.SetBasicAuth("me@example.com", "s3cret-0123456789-wxyz") },
	}
	var out struct{ Issues []any }
	if err := c.Do(context.Background(), http.MethodPost, "/search", map[string]string{"jql": "project = X"}, &out); err != nil {
		t.Fatalf("Do: %v; the trace must pass the response on", err)
	}
	got := log.String()
	for _, want := range []string{"POST " + srv.URL + "/search", "(auth: Basic, user me@example.com, password (22 chars, ending …wxyz))", `{"jql":"project = X"}`, "200 OK", `{"issues":[]}`} {
		if !strings.Contains(got, want) {
			t.Errorf("trace lacks %q:\n%s", want, got)
		}
	}
	for _, secret := range []string{"s3cret", "0123456789"} {
		if strings.Contains(got, secret) {
			t.Errorf("trace contains the credential %q:\n%s", secret, got)
		}
	}
}

func TestDescribeAuth(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
	}{
		{"", "", "none"},
		{"Authorization", "Bearer abcdefghijklmnopqrstuvwxyz", "Bearer (26 chars, ending …wxyz)"},
		// Toggl sends its token as the basic-auth user.
		{"Authorization", "Basic dG9rZW4tYWJjZGVmZ2hpamtsbW5vcDphcGlfdG9rZW4=", "Basic, user (22 chars, ending …mnop), password (9 chars)"},
		{"X-Redmine-API-Key", "0123456789abcdef0123", "X-Redmine-Api-Key (20 chars, ending …0123)"},
	} {
		h := http.Header{}
		if tc.name != "" {
			h.Set(tc.name, tc.value)
		}
		if got := describeAuth(h); got != tc.want {
			t.Errorf("describeAuth(%s: %s) = %q; want %q", tc.name, tc.value, got, tc.want)
		}
	}
}
