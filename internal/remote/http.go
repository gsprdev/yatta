package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client is a small JSON-over-HTTP client shared by the adapters.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Auth sets authentication headers on each request.
	Auth func(*http.Request)
}

// Error is a non-2xx response. Its message carries the remote's own
// explanation where one can be found, since it is shown to the user.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Body)
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, msg)
}

// Do sends body (if non-nil) as JSON and decodes a JSON response into out (if
// non-nil).
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "yatta")
	if c.Auth != nil {
		c.Auth(req)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &Error{Status: resp.StatusCode, Body: explain(data)}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected response from %s: %w", path, err)
	}
	return nil
}

// explain extracts the human-readable messages from the error bodies the
// three remotes return, falling back to the raw body.
func explain(body []byte) string {
	var v struct {
		ErrorMessages []string        `json:"errorMessages"` // Jira
		Errors        json.RawMessage `json:"errors"`        // Jira (map), Redmine (list)
		Message       string          `json:"message"`
	}
	if json.Unmarshal(body, &v) != nil {
		return string(body)
	}
	msgs := append([]string{}, v.ErrorMessages...)
	var list []string
	var fields map[string]string
	if json.Unmarshal(v.Errors, &list) == nil {
		msgs = append(msgs, list...)
	} else if json.Unmarshal(v.Errors, &fields) == nil {
		for k, m := range fields {
			msgs = append(msgs, k+": "+m)
		}
	}
	if v.Message != "" {
		msgs = append(msgs, v.Message)
	}
	if len(msgs) == 0 {
		return string(body)
	}
	return strings.Join(msgs, "; ")
}
