// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/jmap"
)

// squatter listens on loopback as this test's own process and records the
// Authorization header of anything sent to it.
func squatter(t *testing.T) (url string, seen func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// During a host first boot or a move the server is restarted and then sent
// the administrator's password or an API key on 127.0.0.1:8080 or :8081,
// ports any local user may listen on while the server is down. What answers
// there must be the server the unit started before anything is sent to it
// (INSTALLER-4). Here the listener is this test's, and the unit's main
// process is another one.
func TestSetupCredentialsGoOnlyToTheServersOwnListener(t *testing.T) {
	url, seen := squatter(t)
	defer func(f func(context.Context) int) { serverMainPID = f }(serverMainPID)
	serverMainPID = func(context.Context) int { return os.Getppid() }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin := setupClient(true, jmap.Client{BaseURL: url, Username: "admin", Password: "the-admin-password"})
	if _, _, err := admin.CreateAPIKey(ctx, "setup", time.Hour); err == nil {
		t.Error("an API key was made through a listener that isn't the server's")
	}
	keyed := setupClient(true, jmap.Client{BaseURL: url, Token: "the-api-key"})
	if err := keyed.HostListeners(ctx, hostFinalBind, false); err == nil {
		t.Error("the listeners were fitted through a listener that isn't the server's")
	}
	if got := seen(); len(got) != 0 {
		t.Errorf("credentials went to a listener that isn't the server's: %q", got)
	}
}

// When the listener is the server's, the request goes through; and a
// container's server, reached through the runtime's port, is not checked.
func TestSetupCredentialsGoToTheServer(t *testing.T) {
	url, seen := squatter(t)
	defer func(f func(context.Context) int) { serverMainPID = f }(serverMainPID)
	serverMainPID = func(context.Context) int { return os.Getpid() }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	setupClient(true, jmap.Client{BaseURL: url, Token: "k"}).Listeners(ctx)
	serverMainPID = func(context.Context) int { return os.Getppid() }
	setupClient(false, jmap.Client{BaseURL: url, Token: "k"}).Listeners(ctx)
	if got := seen(); len(got) != 2 {
		t.Errorf("want both requests sent, got %q", got)
	}
}

// First boot moves its client from 8080 to 8081 once the listeners are
// fitted; the check follows it there.
func TestSetupCredentialsCheckTheClientsCurrentAddress(t *testing.T) {
	old, _ := squatter(t)
	url, seen := squatter(t)
	defer func(f func(context.Context) int) { serverMainPID = f }(serverMainPID)
	serverMainPID = func(context.Context) int { return os.Getpid() }
	srv := setupClient(true, jmap.Client{BaseURL: old, Token: "k"})
	srv.BaseURL = url
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Listeners(ctx)
	if got := seen(); len(got) != 1 {
		t.Errorf("want the request sent to the new address, got %q", got)
	}
	// Both of the test's listeners are its own, so stand in a listener
	// that is not: a closed one, where nothing listens any more.
	srv.BaseURL = "http://127.0.0.1:1"
	if err := serversOwnListener(ctx, srv.BaseURL); err == nil {
		t.Error("a port nothing listens on passed the check")
	}
}
