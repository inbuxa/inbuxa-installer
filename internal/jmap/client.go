// SPDX-FileCopyrightText: 2026 Coffey Labs
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jmap talks to an inbuxa server over JMAP.
//
// There is no REST management API and no configuration file to template: a
// server with an empty configuration directory starts in bootstrap mode, and
// everything from the first administrator to the ACME account is a registry
// object read and written with x: methods. This client came from
// ihasmail-oneshot, where every call in it was worked out against a running
// server rather than from documentation.
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var using = []string{"urn:ietf:params:jmap:core", "urn:stalwart:jmap"}

// Client calls one Stalwart as one account. The zero HTTP uses a client with a
// timeout, so a hung server fails a step instead of hanging the tool.
type Client struct {
	BaseURL  string // e.g. http://127.0.0.1:8081, no trailing slash
	Username string
	Password string
	HTTP     *http.Client
}

// Call is one method call in a request.
type Call struct {
	Method string
	Args   any
	ID     string
}

// Response is one method response.
type Response struct {
	Method string
	Args   json.RawMessage
	ID     string
}

// MethodError is a JMAP method-level error: the request was fine, the call was
// refused.
type MethodError struct {
	Method      string
	Type        string
	Description string
}

func (e *MethodError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s: %s", e.Method, e.Type, e.Description)
	}
	return fmt.Sprintf("%s: %s", e.Method, e.Type)
}

// HTTPError is a response that was not a JMAP response at all.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, strings.TrimSpace(e.Body))
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Do sends calls in one request and returns their responses in order. A method
// error in any of them is returned as a *MethodError, after the responses
// before it -- JMAP stops nothing on an error, but every caller here needs
// all of its calls to have worked.
func (c *Client) Do(ctx context.Context, calls ...Call) ([]Response, error) {
	mc := make([][3]any, len(calls))
	for i, call := range calls {
		mc[i] = [3]any{call.Method, call.Args, call.ID}
	}
	body, err := json.Marshal(map[string]any{"using": using, "methodCalls": mc})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/jmap/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: res.StatusCode, Body: string(raw)}
	}
	var envelope struct {
		MethodResponses [][3]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("not a JMAP response: %w", err)
	}
	out := make([]Response, 0, len(envelope.MethodResponses))
	for _, mr := range envelope.MethodResponses {
		var r Response
		if err := json.Unmarshal(mr[0], &r.Method); err != nil {
			return nil, fmt.Errorf("not a JMAP response: %w", err)
		}
		if err := json.Unmarshal(mr[2], &r.ID); err != nil {
			return nil, fmt.Errorf("not a JMAP response: %w", err)
		}
		r.Args = mr[1]
		if r.Method == "error" {
			var e struct {
				Type        string `json:"type"`
				Description string `json:"description"`
			}
			_ = json.Unmarshal(r.Args, &e)
			method := r.ID
			for _, call := range calls {
				if call.ID == r.ID {
					method = call.Method
				}
			}
			return out, &MethodError{Method: method, Type: e.Type, Description: e.Description}
		}
		out = append(out, r)
	}
	if len(out) != len(calls) {
		return out, fmt.Errorf("sent %d method calls and got %d responses", len(calls), len(out))
	}
	return out, nil
}

// SetError is one entry of notCreated, notUpdated or notDestroyed.
type SetError struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Properties  []string `json:"properties"`
}

func (e SetError) Error() string {
	s := e.Type
	if e.Description != "" {
		s += ": " + e.Description
	}
	if len(e.Properties) > 0 {
		s += " (" + strings.Join(e.Properties, ", ") + ")"
	}
	return s
}

// SetResult is the part of a /set response every caller here reads.
type SetResult struct {
	Created      map[string]json.RawMessage `json:"created"`
	Updated      map[string]json.RawMessage `json:"updated"`
	NotCreated   map[string]SetError        `json:"notCreated"`
	NotUpdated   map[string]SetError        `json:"notUpdated"`
	NotDestroyed map[string]SetError        `json:"notDestroyed"`
}

// Refused returns the first refusal in the result, if any.
func (r SetResult) Refused() error {
	for _, m := range []map[string]SetError{r.NotCreated, r.NotUpdated, r.NotDestroyed} {
		for id, e := range m {
			return fmt.Errorf("%s: %w", id, e)
		}
	}
	return nil
}

// DecodeSet reads a /set response, turning a not-created or not-updated
// entry into an error rather than a silent success.
func DecodeSet(r Response) (SetResult, error) {
	var s SetResult
	if err := json.Unmarshal(r.Args, &s); err != nil {
		return s, fmt.Errorf("%s: %w", r.Method, err)
	}
	if err := s.Refused(); err != nil {
		return s, fmt.Errorf("%s refused %w", r.Method, err)
	}
	return s, nil
}

// CreatedID reads the server-assigned id of a created object.
func CreatedID(s SetResult, key string) (string, error) {
	raw, ok := s.Created[key]
	if !ok {
		return "", errors.New("the server did not confirm the create")
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.ID == "" {
		return "", errors.New("the server confirmed the create without an id")
	}
	return obj.ID, nil
}

// Live reports whether Stalwart answers its liveness probe, which it does in
// bootstrap mode too.
func Live(ctx context.Context, baseURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz/live", nil)
	if err != nil {
		return err
	}
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &HTTPError{Status: res.StatusCode}
	}
	return nil
}
