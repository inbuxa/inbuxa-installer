// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const wait = 2 * time.Second

// listenTCP opens a throwaway TCP listener and returns its address. If greet
// is non-empty, it's written to the first connection, like a NATS greeting.
func listenTCP(t *testing.T, greet string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if greet != "" {
				conn.Write([]byte(greet))
			}
			conn.Close()
		}
	}()
	return ln.Addr().String()
}

// closedAddr returns an address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestTCPProbe(t *testing.T) {
	open := listenTCP(t, "")
	if ok, _ := tcpProbe(context.Background(), open, wait); !ok {
		t.Error("a listening port should probe OK")
	}
	if ok, detail := tcpProbe(context.Background(), closedAddr(t), wait); ok {
		t.Errorf("a closed port should fail, got OK (%s)", detail)
	}
}

func TestNATSProbe(t *testing.T) {
	nats := listenTCP(t, "INFO {\"server_id\":\"x\"}\r\n")
	if ok, detail := natsProbe(context.Background(), nats, wait); !ok {
		t.Errorf("a NATS greeting should probe OK, got %q", detail)
	}
	// A port that's open but doesn't greet like NATS fails.
	notNats := listenTCP(t, "+OK\r\n")
	if ok, _ := natsProbe(context.Background(), notNats, wait); ok {
		t.Error("a non-NATS greeting should fail")
	}
	if ok, _ := natsProbe(context.Background(), closedAddr(t), wait); ok {
		t.Error("a closed port should fail")
	}
}

func TestHTTPProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/primary":
			w.WriteHeader(http.StatusServiceUnavailable) // not the leader
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	// Any 2xx when the wanted status is 0.
	if ok, detail := httpProbe(context.Background(), srv.URL+"/health", 0, wait); !ok {
		t.Errorf("/health 200 should pass, got %q", detail)
	}
	// Patroni: 200 means leader; 503 (not leader) must fail when 200 is wanted.
	if ok, _ := httpProbe(context.Background(), srv.URL+"/primary", 200, wait); ok {
		t.Error("/primary 503 should fail when 200 is wanted")
	}
	if ok, _ := httpProbe(context.Background(), srv.URL+"/missing", 0, wait); ok {
		t.Error("404 should fail")
	}
}

func TestDoctorAndAllOK(t *testing.T) {
	open := listenTCP(t, "")
	checks := Doctor(nil, []Target{
		{Name: "PostgreSQL", Kind: TCP, Addr: open},
		{Name: "NATS", Kind: TCP, Addr: closedAddr(t)},
	}, wait)
	if len(checks) != 2 {
		t.Fatalf("want 2 checks, got %d", len(checks))
	}
	if !checks[0].OK || checks[1].OK {
		t.Errorf("unexpected results: %+v", checks)
	}
	if AllOK(checks) {
		t.Error("AllOK should be false when one check failed")
	}
	if !AllOK(checks[:1]) {
		t.Error("AllOK should be true when every check passed")
	}
}
