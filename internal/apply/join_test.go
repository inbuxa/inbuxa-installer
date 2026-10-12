// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
)

// fakeServer answers as an inbuxa server would for join: a session for the
// right key, and inbuxa:FrontEnds/get with what frontEnds holds. A nil
// frontEnds is a server too old to have it.
func fakeServer(t *testing.T, frontEnds map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/jmap/session":
			w.Write([]byte(`{"capabilities":{"urn:ietf:params:jmap:core":{},"urn:inbuxa:jmap":{}},
				"primaryAccounts":{"urn:inbuxa:jmap":"admin1"}}`))
		case "/jmap/":
			var req struct {
				MethodCalls [][3]json.RawMessage `json:"methodCalls"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			var method string
			json.Unmarshal(req.MethodCalls[0][0], &method)
			if method != "inbuxa:FrontEnds/get" {
				t.Errorf("join's check sent %s", method)
			}
			if frontEnds == nil {
				w.Write([]byte(`{"methodResponses":[["error",{"type":"unknownMethod"},"0"]]}`))
				return
			}
			b, _ := json.Marshal(map[string]any{"methodResponses": []any{
				[]any{"inbuxa:FrontEnds/get", map[string]any{"list": []any{frontEnds}}, "0"},
			}})
			w.Write(b)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func joinOptions(serverURL string, replace bool) plan.Options {
	return plan.Options{
		ServerURL: serverURL, Domain: "example.com", Replace: replace,
		ConsoleHost: "admin.example.com", WebmailHost: "webmail.example.com",
		Choices: []plan.Choice{
			{Component: plan.Server, Shape: plan.Skip},
			{Component: plan.Console, Shape: plan.Container},
			{Component: plan.Webmail, Shape: plan.Container},
		},
	}
}

func TestJoinCheck(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		frontEnds map[string]any
		key       string
		replace   bool
		want      string // a word the refusal must contain; "" for none
	}{
		{"a fresh server takes them", map[string]any{"environmentWebmailClientSecret": false}, "good-key", false, ""},
		{"the same addresses again", map[string]any{"adminUrl": "https://admin.example.com", "webmailUrl": "https://webmail.example.com"}, "good-key", false, ""},
		{"a wrong key", map[string]any{}, "bad-key", false, "API key"},
		{"a server too old", nil, "good-key", false, "or newer"},
		{"another console listed", map[string]any{"adminUrl": "https://other.example.com"}, "good-key", false, "--replace"},
		{"another console, replaced", map[string]any{"adminUrl": "https://other.example.com"}, "good-key", true, ""},
		{"another webmail listed", map[string]any{"webmailUrl": "https://mail-ui.example.com"}, "good-key", false, "--replace"},
		{"the server's environment holds the secret", map[string]any{"environmentWebmailClientSecret": true}, "good-key", true, "INBUXA_WEBMAIL_CLIENT_SECRET"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeServer(t, tc.frontEnds)
			err := JoinCheck(ctx, joinOptions(srv.URL, tc.replace), tc.key)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.want != "" && err == nil:
				t.Errorf("accepted, want a refusal naming %q", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Errorf("refusal %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestJoinWithoutAWebmailIgnoresItsSecret(t *testing.T) {
	srv := fakeServer(t, map[string]any{"environmentWebmailClientSecret": true, "webmailUrl": "https://elsewhere.example.com"})
	o := joinOptions(srv.URL, false)
	o.Choices[2].Shape = plan.Skip
	if err := JoinCheck(context.Background(), o, "good-key"); err != nil {
		t.Errorf("a console-only join was refused over the webmail: %v", err)
	}
}

func TestJoinURLs(t *testing.T) {
	o := joinOptions("https://mail.example.com", false)
	if c, w := joinURLs(o); c != "https://admin.example.com" || w != "https://webmail.example.com" {
		t.Errorf("urls = %s, %s", c, w)
	}
	o.Local = true
	if c, w := joinURLs(o); c != "http://127.0.0.1:8082" || w != "http://127.0.0.1:8080" {
		t.Errorf("local urls = %s, %s", c, w)
	}
}
