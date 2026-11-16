// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fake answers each request with the response registered for its first
// method, and records what it was sent.
func fake(t *testing.T, responses map[string]string) (*Client, *[]map[string]any) {
	t.Helper()
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The password, or the API key a configured server takes instead.
		if u, p, _ := r.BasicAuth(); (u != "admin" || p != "pw") && r.Header.Get("Authorization") != "Bearer API_key" {
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
	a, err := c.Bootstrap(context.Background(), "mail.example.com", "example.com", "")
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
	if using := (*seen)[0]["using"].([]string); len(using) != 2 || using[1] != "urn:inbuxa:jmap:registry" {
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

func TestAPIKeyIsCreatedWithAnExpiryAndThenSentAsBearer(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:ApiKey/set": `{"methodResponses":[["x:ApiKey/set",{"created":{"key":{"id":"k1","secret":"API_key"}},"destroyed":["k1"]},"0"]]}`,
	})
	id, token, err := c.CreateAPIKey(context.Background(), "setup", time.Hour)
	if err != nil || id != "k1" || token != "API_key" {
		t.Fatalf("id %q, token %q, err %v", id, token, err)
	}
	key := (*seen)[0]["args"].(map[string]any)["create"].(map[string]any)["key"].(map[string]any)
	if exp, _ := key["expiresAt"].(string); exp == "" {
		t.Errorf("no expiresAt: a key left by a failed setup would never lapse")
	}

	bearer := &Client{BaseURL: c.BaseURL, Token: token}
	if err := bearer.DestroyAPIKey(context.Background(), id); err != nil {
		t.Fatalf("destroy with the key as Bearer: %v", err)
	}
	if got := (*seen)[1]["args"].(map[string]any)["destroy"].([]any); len(got) != 1 || got[0] != "k1" {
		t.Errorf("destroy = %v", got)
	}
}

func TestBearerReplacesBasic(t *testing.T) {
	c, _ := fake(t, nil)
	c.Token = "API_wrong" // with the right password still set
	if _, err := c.Do(context.Background(), Call{"x:Domain/query", map[string]any{}, "0"}); err == nil {
		t.Fatal("a wrong token was accepted: the password must not be sent alongside it")
	}
}

func TestBootstrapOnTheHostNamesItsDataDirectory(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:Bootstrap/set": `{"methodResponses":[["x:Bootstrap/set",{"updated":{"singleton":{"username":"admin@example.com","secret":"s3cret"}}},"0"]]}`,
	})
	if _, err := c.Bootstrap(context.Background(), "mail.example.com", "example.com", "/var/lib/inbuxa/server/data/"); err != nil {
		t.Fatal(err)
	}
	update := (*seen)[0]["args"].(map[string]any)["update"].(map[string]any)["singleton"].(map[string]any)
	store, _ := update["dataStore"].(map[string]any)
	if store["@type"] != "RocksDb" || store["path"] != "/var/lib/inbuxa/server/data/" {
		t.Errorf("dataStore = %v", update["dataStore"])
	}
}

const defaultListeners = `{"methodResponses":[
	["x:NetworkListener/query",{"ids":["a","b","c"]},"0"],
	["x:NetworkListener/get",{"list":[
		{"id":"a","name":"smtp","protocol":"smtp","tlsImplicit":false,"bind":{"[::]:25":true}},
		{"id":"b","name":"https","protocol":"http","tlsImplicit":true,"bind":{"[::]:443":true}},
		{"id":"c","name":"http","protocol":"http","tlsImplicit":false,"bind":{"[::]:8080":true}}
	]},"1"]]}`

func TestHostListenersMoveHTTPAndLeave443ToTheProxy(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:NetworkListener/query": defaultListeners,
		"x:NetworkListener/set":   `{"methodResponses":[["x:NetworkListener/set",{"updated":{"c":null},"destroyed":["b"]},"0"]]}`,
	})
	if err := c.HostListeners(context.Background(), "127.0.0.1:8081", false); err != nil {
		t.Fatal(err)
	}
	args := (*seen)[1]["args"].(map[string]any)
	update := args["update"].(map[string]any)
	if len(update) != 1 {
		t.Fatalf("updated %v, want only the plain HTTP listener", update)
	}
	bind := update["c"].(map[string]any)["bind"].(map[string]any)
	if len(bind) != 1 || bind["127.0.0.1:8081"] != true {
		t.Errorf("http bind = %v", bind)
	}
	if d := args["destroy"].([]any); len(d) != 1 || d[0] != "b" {
		t.Errorf("destroy = %v, want the HTTPS listener", d)
	}
}

func TestHostListenersOnLoopbackForALocalInstall(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:NetworkListener/query": defaultListeners,
		"x:NetworkListener/set":   `{"methodResponses":[["x:NetworkListener/set",{"updated":{"a":null,"c":null},"destroyed":["b"]},"0"]]}`,
	})
	if err := c.HostListeners(context.Background(), "127.0.0.1:8081", true); err != nil {
		t.Fatal(err)
	}
	update := (*seen)[1]["args"].(map[string]any)["update"].(map[string]any)
	bind := update["a"].(map[string]any)["bind"].(map[string]any)
	if len(bind) != 1 || bind["127.0.0.1:25"] != true {
		t.Errorf("smtp bind = %v, want loopback", bind)
	}
}

func TestHostListenersRefusalIsAnError(t *testing.T) {
	c, _ := fake(t, map[string]string{
		"x:NetworkListener/query": defaultListeners,
		"x:NetworkListener/set":   `{"methodResponses":[["x:NetworkListener/set",{"notDestroyed":{"b":{"type":"forbidden","description":"no"}}},"0"]]}`,
	})
	if err := c.HostListeners(context.Background(), "127.0.0.1:8081", false); err == nil {
		t.Fatal("a refused destroy was not reported")
	}
}

func TestPassThroughListenerTakesPROXYFromTheWebServerOnly(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:NetworkListener/query": defaultListeners,
		"x:NetworkListener/set":   `{"methodResponses":[["x:NetworkListener/set",{"updated":{"b":null}},"0"]]}`,
	})
	if err := c.PassThroughListener(context.Background(), "127.0.0.1:8443", []string{"127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	args := (*seen)[1]["args"].(map[string]any)
	update := args["update"].(map[string]any)
	if len(update) != 1 || args["create"] != nil {
		t.Fatalf("set %v, want only the HTTPS listener updated", args)
	}
	b := update["b"].(map[string]any)
	if bind := b["bind"].(map[string]any); len(bind) != 1 || bind["127.0.0.1:8443"] != true {
		t.Errorf("bind = %v", bind)
	}
	if nets := b["overrideProxyTrustedNetworks"].(map[string]any); len(nets) != 1 || nets["127.0.0.1/32"] != true {
		t.Errorf("trusted = %v, want the web server's address alone", nets)
	}
}

func TestPassThroughListenerPutsBackAnHTTPSListener(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:NetworkListener/query": `{"methodResponses":[["x:NetworkListener/query",{"ids":["c"]},"0"],["x:NetworkListener/get",{"list":[
			{"id":"c","name":"http","protocol":"http","tlsImplicit":false,"bind":{"127.0.0.1:8081":true}}]},"1"]]}`,
		"x:NetworkListener/set": `{"methodResponses":[["x:NetworkListener/set",{"created":{"https":{"id":"d"}}},"0"]]}`,
	})
	if err := c.PassThroughListener(context.Background(), "[::]:443", []string{"172.31.253.1"}); err != nil {
		t.Fatal(err)
	}
	args := (*seen)[1]["args"].(map[string]any)
	if args["update"] != nil {
		t.Errorf("the plain HTTP listener was changed: %v", args["update"])
	}
	https := args["create"].(map[string]any)["https"].(map[string]any)
	if https["protocol"] != "http" || https["tlsImplicit"] != true || https["useTls"] != true {
		t.Errorf("created %v, want an implicit-TLS HTTP listener", https)
	}
	if nets := https["overrideProxyTrustedNetworks"].(map[string]any); nets["172.31.253.1/32"] != true {
		t.Errorf("trusted = %v", nets)
	}
}

// tracers is an x:Tracer/query and /get answer listing ts.
func tracers(ts ...string) string {
	ids := make([]string, len(ts))
	for i := range ts {
		ids[i] = fmt.Sprintf("%q", fmt.Sprint(i))
	}
	return `{"methodResponses":[["x:Tracer/query",{"ids":[` + strings.Join(ids, ",") + `]},"0"],` +
		`["x:Tracer/get",{"list":[` + strings.Join(ts, ",") + `]},"1"]]}`
}

const stdoutTracer = `{"id":"a","@type":"Stdout","enable":true,"level":"info"}`

// An installed server has only the stdout tracer bootstrap gave it, so the
// Log tracer the Logs page reads is created beside it, as the server's own
// default would have been (#70).
func TestEnsureLogTracerAddsOneBesideStdout(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:Tracer/query": tracers(stdoutTracer),
		"x:Tracer/set":   `{"methodResponses":[["x:Tracer/set",{"created":{"log":{"id":"b"}}},"0"]]}`,
	})
	got, err := c.EnsureLogTracer(context.Background(), "/var/lib/inbuxa/logs")
	if err != nil || got != LogTracerAdded {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(*seen) != 2 {
		t.Fatalf("%d requests", len(*seen))
	}
	log := (*seen)[1]["args"].(map[string]any)["create"].(map[string]any)["log"].(map[string]any)
	for k, want := range map[string]any{
		"@type": "Log", "enable": true, "path": "/var/lib/inbuxa/logs",
		"prefix": "inbuxa.log", "rotate": "daily", "level": "info", "ansi": false,
	} {
		if log[k] != want {
			t.Errorf("%s = %v, want %v", k, log[k], want)
		}
	}
}

// Run again, nothing is written.
func TestEnsureLogTracerLeavesOneThatIsThere(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:Tracer/query": tracers(stdoutTracer, `{"id":"b","@type":"Log","enable":true,"path":"/var/log/inbuxa/","prefix":"inbuxa.log"}`),
	})
	got, err := c.EnsureLogTracer(context.Background(), "/var/log/inbuxa", "/var/lib/inbuxa/logs")
	if err != nil || got != LogTracerPresent || len(*seen) != 1 {
		t.Fatalf("got %v, %v after %d requests", got, err, len(*seen))
	}
}

// A server moved between a container and the host takes the installer's
// tracer with it, pointed at the directory its new shape has.
func TestEnsureLogTracerMovesTheInstallersOwn(t *testing.T) {
	c, seen := fake(t, map[string]string{
		"x:Tracer/query": tracers(stdoutTracer, `{"id":"b","@type":"Log","enable":true,"path":"/var/lib/inbuxa/logs","prefix":"inbuxa.log"}`),
		"x:Tracer/set":   `{"methodResponses":[["x:Tracer/set",{"updated":{"b":null}},"0"]]}`,
	})
	got, err := c.EnsureLogTracer(context.Background(), "/var/log/inbuxa", "/var/lib/inbuxa/logs")
	if err != nil || got != LogTracerMoved {
		t.Fatalf("got %v, %v", got, err)
	}
	update := (*seen)[1]["args"].(map[string]any)["update"].(map[string]any)
	if b, ok := update["b"].(map[string]any); !ok || b["path"] != "/var/log/inbuxa" || len(update) != 1 {
		t.Errorf("update = %v", update)
	}
}

// A Log tracer an administrator set up is theirs: one writing elsewhere, one
// turned off, or one in the other shape's directory under a prefix of their
// own is left alone, and nothing is added beside it.
func TestEnsureLogTracerKeepsAnAdministrators(t *testing.T) {
	for _, log := range []string{
		`{"id":"b","@type":"Log","enable":true,"path":"/srv/logs","prefix":"inbuxa.log"}`,
		`{"id":"b","@type":"Log","enable":false,"path":"/var/log/inbuxa","prefix":"inbuxa.log"}`,
		`{"id":"b","@type":"Log","enable":true,"path":"/var/lib/inbuxa/logs","prefix":"mail"}`,
	} {
		c, seen := fake(t, map[string]string{"x:Tracer/query": tracers(stdoutTracer, log)})
		got, err := c.EnsureLogTracer(context.Background(), "/var/log/inbuxa", "/var/lib/inbuxa/logs")
		if err != nil || got != LogTracerKept || len(*seen) != 1 {
			t.Errorf("%s: got %v, %v after %d requests", log, got, err, len(*seen))
		}
	}
}

func TestEnsureLogTracerRefusalIsAnError(t *testing.T) {
	c, _ := fake(t, map[string]string{
		"x:Tracer/query": tracers(stdoutTracer),
		"x:Tracer/set":   `{"methodResponses":[["x:Tracer/set",{"notCreated":{"log":{"type":"invalidProperties","properties":["path"]}}},"0"]]}`,
	})
	if _, err := c.EnsureLogTracer(context.Background(), "/var/log/inbuxa"); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("err = %v", err)
	}
}
