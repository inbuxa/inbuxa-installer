// SPDX-FileCopyrightText: 2026 Coffey Labs
// SPDX-License-Identifier: AGPL-3.0-or-later

package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fake answers each request with the response registered for its first
// method, and records what it was sent.
func fake(t *testing.T, responses map[string]string) (*Client, *[]map[string]any) {
	t.Helper()
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "admin" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			Using       []string             `json:"using"`
			MethodCalls [][3]json.RawMessage `json:"methodCalls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		var method string
		_ = json.Unmarshal(req.MethodCalls[0][0], &method)
		var args map[string]any
		_ = json.Unmarshal(req.MethodCalls[0][1], &args)
		seen = append(seen, map[string]any{"method": method, "args": args, "using": req.Using})
		body, ok := responses[method]
		if !ok {
			t.Errorf("unexpected method %s", method)
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, Username: "admin", Password: "pw"}, &seen
}

func TestBootstrapReturnsTheAdministrator(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:Bootstrap/set": `{"methodResponses":[["x:Bootstrap/set",{"updated":{"singleton":{"username":"admin@example.com","secret":"s3cret"}}},"0"]]}`,
	})
	a, err := c.Bootstrap(context.Background(), "mail.example.com", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a.Username != "admin@example.com" || a.Secret != "s3cret" {
		t.Errorf("admin = %+v", a)
	}
	update := (*seen)[0]["args"].(map[string]any)["update"].(map[string]any)["singleton"].(map[string]any)
	if update["requestTlsCertificate"] != false || update["tracer"].(map[string]any)["@type"] != "Stdout" {
		t.Errorf("bootstrap sent %v", update)
	}
	if using := (*seen)[0]["using"].([]string); len(using) != 2 || using[1] != "urn:stalwart:jmap" {
		t.Errorf("using = %v", using)
	}
}

func TestSetRefusalIsAnError(t *testing.T) {
	c, _ := fake(t, map[string]string{
		"x:Account/set": `{"methodResponses":[["x:Account/set",{"notCreated":{"user":{"type":"invalidPatch","description":"Missing or invalid '@type' property in object","properties":["roles"]}}},"0"]]}`,
	})
	_, err := c.CreateUser(context.Background(), "alice", "b", "pw")
	if err == nil || !strings.Contains(err.Error(), "invalidPatch") || !strings.Contains(err.Error(), "roles") {
		t.Fatalf("err = %v", err)
	}
}

func TestMethodErrorNamesTheMethod(t *testing.T) {
	c, _ := fake(t, map[string]string{
		"x:Bootstrap/get": `{"methodResponses":[["error",{"type":"unknownMethod"},"0"]]}`,
	})
	err := c.CheckBootstrapMode(context.Background())
	var me *MethodError
	if !errors.As(err, &me) || me.Method != "x:Bootstrap/get" || me.Type != "unknownMethod" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "not in bootstrap mode") {
		t.Errorf("err = %v", err)
	}
}

func TestConfiguredServerRefusesBootstrapCredentials(t *testing.T) {
	c, _ := fake(t, nil)
	c.Password = "the-bootstrap-password"
	if err := c.CheckBootstrapMode(context.Background()); err == nil || !strings.Contains(err.Error(), "not in bootstrap mode") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnableACMEUsesHTTP01AndTheBackReference(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:AcmeProvider/set": `{"methodResponses":[["x:AcmeProvider/set",{"created":{"acme":{"id":"p1"}}},"0"],["x:Domain/set",{"updated":{"b":null}},"1"]]}`,
	})
	id, err := c.EnableACME(context.Background(), "b", "", "postmaster@example.com")
	if err != nil || id != "p1" {
		t.Fatalf("id %q, err %v", id, err)
	}
	provider := (*seen)[0]["args"].(map[string]any)["create"].(map[string]any)["acme"].(map[string]any)
	if provider["challengeType"] != "Http01" {
		t.Errorf("challengeType = %v", provider["challengeType"])
	}
	if _, set := provider["directory"]; set {
		t.Error("directory sent without --acme-directory; Stalwart's default is Let's Encrypt")
	}
}

func TestDomainIDNotFound(t *testing.T) {
	c, _ := fake(t, map[string]string{
		"x:Domain/query": `{"methodResponses":[["x:Domain/query",{"ids":["b"]},"0"],["x:Domain/get",{"list":[{"id":"b","name":"other.test"}]},"1"]]}`,
	})
	if _, err := c.DomainID(context.Background(), "example.com"); err == nil {
		t.Fatal("found a domain that is not there")
	}
}
