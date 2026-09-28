package remote

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// traceLimit caps how much of each body is written to the trace.
const traceLimit = 16 << 10

// Trace wraps next so that every request and response is written to w, for
// diagnosing an integration. Of the headers only the credentials are
// described, and only by Fingerprint, so the trace never holds one in full.
func Trace(w io.Writer, next http.RoundTripper) http.RoundTripper {
	return &tracer{w: w, next: next}
}

type tracer struct {
	mu   sync.Mutex
	w    io.Writer
	next http.RoundTripper
}

func (t *tracer) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		reqBody = b
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	auth := describeAuth(req.Header)
	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	elapsed := time.Since(start).Round(time.Millisecond)

	var b strings.Builder
	fmt.Fprintf(&b, "=== %s %s %s (auth: %s)\n", start.Format(time.RFC3339), req.Method, req.URL, auth)
	if len(reqBody) > 0 {
		fmt.Fprintf(&b, "%s\n", clip(reqBody))
	}
	if err == nil {
		var respBody []byte
		respBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		fmt.Fprintf(&b, "--- %s after %s\n%s\n", resp.Status, elapsed, clip(respBody))
	}
	if err != nil {
		resp = nil
		fmt.Fprintf(&b, "--- error after %s: %v\n", elapsed, err)
	}
	b.WriteString("\n")
	t.mu.Lock()
	io.WriteString(t.w, b.String())
	t.mu.Unlock()
	return resp, err
}

// Fingerprint describes a secret without revealing it: its length and, when
// long enough that little is given away, its last four characters. That is
// enough to tell which of several tokens was sent.
func Fingerprint(secret string) string {
	switch n := len(secret); {
	case n == 0:
		return "(empty)"
	case n < 16:
		return fmt.Sprintf("(%d chars)", n)
	default:
		return fmt.Sprintf("(%d chars, ending …%s)", n, secret[n-4:])
	}
}

// describeAuth says which credentials a request carries. A basic-auth user
// is shown when it is an email address; otherwise it may be a token itself,
// as with Toggl.
func describeAuth(h http.Header) string {
	var parts []string
	if v := h.Get("Authorization"); v != "" {
		scheme, cred, _ := strings.Cut(v, " ")
		if raw, err := base64.StdEncoding.DecodeString(cred); err == nil && strings.EqualFold(scheme, "Basic") {
			user, pass, _ := strings.Cut(string(raw), ":")
			if !strings.Contains(user, "@") {
				user = Fingerprint(user)
			}
			parts = append(parts, fmt.Sprintf("Basic, user %s, password %s", user, Fingerprint(pass)))
		} else {
			parts = append(parts, scheme+" "+Fingerprint(cred))
		}
	}
	for name, vs := range h {
		if n := strings.ToLower(name); strings.Contains(n, "key") || strings.Contains(n, "token") {
			parts = append(parts, name+" "+Fingerprint(strings.Join(vs, "")))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	slices.Sort(parts)
	return strings.Join(parts, "; ")
}

func clip(b []byte) string {
	if len(b) > traceLimit {
		return fmt.Sprintf("%s… (%d bytes)", b[:traceLimit], len(b))
	}
	return string(b)
}
