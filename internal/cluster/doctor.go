// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cluster checks whether a node can reach the data layer a clustered
// inbuxa server depends on: the PostgreSQL store, the NATS coordinator, the
// S3 blob store, and the etcd and Patroni that stand behind them. It probes
// only endpoints the caller names, so it fits any deployment; the probe each
// one uses follows that service's own convention, not any one cluster's
// layout.
package cluster

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Kind is how an endpoint is probed.
type Kind int

const (
	// TCP just opens a connection: the service is listening.
	TCP Kind = iota
	// NATS opens a connection and reads the server's INFO greeting.
	NATS
	// HTTP makes a GET and checks the status (HealthyStatus, or any 2xx when
	// that is zero).
	HTTP
)

// Target is one endpoint to probe.
type Target struct {
	Name string // what to call it in the report, e.g. "PostgreSQL"
	Kind Kind
	Addr string // host:port for TCP and NATS; a URL for HTTP
	// HealthyStatus is the HTTP status that means healthy; 0 accepts any 2xx.
	// For Patroni's /primary, 200 means this node is the leader and 503 that
	// it isn't, so the caller sets it to 200.
	HealthyStatus int
}

// Check is the result of probing one target.
type Check struct {
	Name   string
	Addr   string
	OK     bool
	Detail string
}

// Doctor probes every target, in order, and returns a Check for each. A nil
// context uses the background context.
func Doctor(ctx context.Context, targets []Target, timeout time.Duration) []Check {
	if ctx == nil {
		ctx = context.Background()
	}
	checks := make([]Check, 0, len(targets))
	for _, t := range targets {
		checks = append(checks, probe(ctx, t, timeout))
	}
	return checks
}

// AllOK reports whether every check passed.
func AllOK(checks []Check) bool {
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}
	return true
}

func probe(ctx context.Context, t Target, timeout time.Duration) Check {
	c := Check{Name: t.Name, Addr: t.Addr}
	switch t.Kind {
	case NATS:
		c.OK, c.Detail = natsProbe(ctx, t.Addr, timeout)
	case HTTP:
		c.OK, c.Detail = httpProbe(ctx, t.Addr, t.HealthyStatus, timeout)
	default:
		c.OK, c.Detail = tcpProbe(ctx, t.Addr, timeout)
	}
	return c
}

func tcpProbe(ctx context.Context, addr string, timeout time.Duration) (bool, string) {
	conn, err := dial(ctx, addr, timeout)
	if err != nil {
		return false, reason(err)
	}
	conn.Close()
	return true, "listening"
}

// natsProbe confirms the port answers like NATS: the server sends an `INFO`
// line as soon as a client connects.
func natsProbe(ctx context.Context, addr string, timeout time.Duration) (bool, string) {
	conn, err := dial(ctx, addr, timeout)
	if err != nil {
		return false, reason(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return false, "connected, but no greeting: " + reason(err)
	}
	if strings.HasPrefix(strings.TrimSpace(line), "INFO") {
		return true, "NATS greeting"
	}
	return false, "connected, but the greeting wasn't NATS"
}

func httpProbe(ctx context.Context, url string, want int, timeout time.Duration) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, reason(err)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return false, reason(err)
	}
	defer resp.Body.Close()
	ok := resp.StatusCode == want || (want == 0 && resp.StatusCode >= 200 && resp.StatusCode < 300)
	return ok, fmt.Sprintf("HTTP %d", resp.StatusCode)
}

func dial(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
}

// reason trims a dial error to the part worth showing.
func reason(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return msg[i+2:]
	}
	return msg
}
