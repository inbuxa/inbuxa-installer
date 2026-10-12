// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

// Certs is the mail server's own certificate: the one its mail ports present
// to clients, which the proxy in front never sees. The console's and the
// webmail's are the proxy's, and it renews those itself.
//
// Looking needs nothing: the certificate is read off port 993 the way a mail
// client reads it. Doing something about it needs an API key made in the
// console, as join does. The server takes no password on its API once it is
// set up, and switching that back on for a moment, as the install's setup
// does, is not something to do to a server that is already live.
//
// What it does about it: a domain whose certificates are on ACME gets a new
// order, which is the one thing the server does not do by itself once an
// order has failed -- the usual story is that DNS was not in place during
// the install. A domain not on ACME is put on it, if something here answers
// the server's challenges on port 80: the managed Caddy, or the operator's
// own web server loading the configuration written for it.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/jmap"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

// Served is a certificate as a mail client is given it.
type Served struct {
	Port       string
	Issuer     string
	NotBefore  time.Time
	NotAfter   time.Time
	Names      []string
	SelfSigned bool
	Trust      error // why it doesn't verify for the mail host; nil when it does
}

// stuckBelow is how little of its lifetime a certificate may have left
// before that is reported as a problem. The server renews with a third left
// (ACME's renewBefore is two thirds of the way through), so a certificate
// down to a sixth has missed its renewal. A fraction rather than a number of
// days, because lifetimes differ: Let's Encrypt's are moving from 90 days
// towards six, and a test CA's are shorter still.
const stuckBelow = 6

// Problem is what is wrong with it for host, in a phrase; "" when nothing is.
func (s *Served) Problem(host string, now time.Time) string {
	switch {
	case s.SelfSigned:
		return "it is the server's own self-signed certificate, which mail clients refuse"
	case now.After(s.NotAfter):
		return "it expired on " + s.NotAfter.Format("2006-01-02")
	case !covers(s.Names, host):
		return "it does not name " + host
	case s.Trust != nil:
		return "it does not verify: " + s.Trust.Error()
	case s.NotAfter.Sub(now) < s.NotAfter.Sub(s.NotBefore)/stuckBelow:
		return "it expires " + inWords(s.NotAfter.Sub(now)) + " and has not been renewed"
	}
	return ""
}

func inWords(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("in %d days", int(d.Hours()/24))
	}
	return fmt.Sprintf("in %d hours", int(d.Hours()))
}

func covers(names []string, host string) bool {
	for _, n := range names {
		if strings.EqualFold(n, host) {
			return true
		}
		if rest, ok := strings.CutPrefix(n, "*."); ok {
			if _, parent, ok := strings.Cut(host, "."); ok && strings.EqualFold(parent, rest) {
				return true
			}
		}
	}
	return false
}

// ServedCertificate reads the certificate the server presents for host at
// addr (host:port), as a client would, and checks it against roots: the
// system's when nil.
func ServedCertificate(ctx context.Context, addr, host string, roots *x509.CertPool) (*Served, error) {
	d := tls.Dialer{Config: &tls.Config{ServerName: host, InsecureSkipVerify: true}}
	d.NetDialer = &net.Dialer{Timeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	chain := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return nil, errors.New("no certificate was presented")
	}
	leaf := chain[0]
	_, port, _ := net.SplitHostPort(addr)
	s := &Served{
		Port:       port,
		Issuer:     issuerName(leaf),
		NotBefore:  leaf.NotBefore,
		NotAfter:   leaf.NotAfter,
		Names:      leaf.DNSNames,
		SelfSigned: bytes.Equal(leaf.RawIssuer, leaf.RawSubject),
	}
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	_, s.Trust = leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: inter})
	return s, nil
}

func issuerName(c *x509.Certificate) string {
	name := c.Issuer.CommonName
	if len(c.Issuer.Organization) > 0 && !strings.Contains(name, c.Issuer.Organization[0]) {
		if name == "" {
			return c.Issuer.Organization[0]
		}
		name += " (" + c.Issuer.Organization[0] + ")"
	}
	if name == "" {
		name = c.Issuer.String()
	}
	return name
}

// CertRoots are the roots this machine trusts, plus the private CA's root an
// install with --acme-ca-root was given, which the server was told to trust
// and so should this.
func CertRoots(dir string) *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if b, err := os.ReadFile(filepath.Join(dir, "ca-bundle.crt")); err == nil {
		pool.AppendCertsFromPEM(b)
	}
	return pool
}

// MailPorts are where the certificate is read: IMAP over TLS, then
// submission over TLS, whichever answers first.
var MailPorts = []string{"993", "465"}

// ReadServed reads the certificate off the first mail port that answers.
func ReadServed(ctx context.Context, host string, roots *x509.CertPool) (*Served, error) {
	var errs []string
	for _, p := range MailPorts {
		s, err := ServedCertificate(ctx, net.JoinHostPort("127.0.0.1", p), host, roots)
		if err == nil {
			return s, nil
		}
		errs = append(errs, p+": "+err.Error())
	}
	return nil, fmt.Errorf("the mail ports don't answer with TLS (%s)", strings.Join(errs, "; "))
}

// CertsMailHost is the server's name: as given, as recorded, as the
// deployment says, or the default.
func CertsMailHost(given string, st *state.State, found string) string {
	for _, h := range []string{given, st.MailHost, found} {
		if h != "" {
			return h
		}
	}
	return "mail." + st.Domain
}

// LocalInstall is whether the install here was --local: recorded since
// 2026.10.13, and before that read off the address the server was given for
// itself, which only a local install sets.
func LocalInstall(st *state.State) bool {
	if st.Local {
		return true
	}
	for _, f := range []string{"compose.yaml", "server.env"} {
		if b, err := os.ReadFile(filepath.Join(st.Dir, f)); err == nil && strings.Contains(string(b), "INBUXA_PUBLIC_URL") {
			return true
		}
	}
	return false
}

// ErrKeyRefused is an API key the server doesn't take.
var ErrKeyRefused = errors.New("the server refused the API key: make one in the console (your account, API keys) as an administrator")

// CertsOptions are what Renew needs beyond the install record.
type CertsOptions struct {
	MailHost      string
	Key           string
	Proxy         string // "caddy", "snippets" or "" for none: what answers on port 80
	Email         string // the ACME contact when ACME is turned on here
	ACMEDirectory string
}

// Renew gets the server a certificate: a new order on ACME, or ACME turned
// on first where something here can pass its challenges. It returns what the
// mail ports present afterwards.
func Renew(ctx context.Context, st *state.State, o CertsOptions, log Log) (*Served, error) {
	srv := &jmap.Client{BaseURL: "http://" + hostFinalBind, Token: o.Key}
	domainID, err := srv.DomainID(ctx, st.Domain)
	if err != nil {
		var he *jmap.HTTPError
		if errors.As(err, &he) && (he.Status == 401 || he.Status == 403) {
			return nil, ErrKeyRefused
		}
		return nil, fmt.Errorf("finding %s on the server: %w", st.Domain, err)
	}
	kind, _, err := srv.CertificateManagement(ctx, domainID)
	if err != nil {
		return nil, err
	}
	if kind == "Automatic" {
		log.Step("starting a new certificate order for %s", o.MailHost)
		if err := srv.RetryCertificates(ctx, domainID); err != nil {
			return nil, err
		}
	} else {
		if o.Proxy == "" {
			return nil, fmt.Errorf("the domain's certificates are %s, and nothing here passes the server's ACME challenges on port 80 "+
				"(the install has no proxy): its certificate is yours to arrange", strings.ToLower(kind))
		}
		email := o.Email
		if email == "" {
			email = "postmaster@" + st.Domain
		}
		log.Step("turning on certificates for %s, from ACME (contact %s)", o.MailHost, email)
		if o.Proxy == "snippets" {
			log.Info("the challenges reach the server through your web server, so it must be loading %s first",
				filepath.Join(st.Dir, "proxy"))
		}
		if _, err := srv.EnableACME(ctx, domainID, o.ACMEDirectory, email); err != nil {
			return nil, fmt.Errorf("enabling ACME: %w", err)
		}
	}
	// Waited for as the mail ports present it, not as the server lists it:
	// a certificate about to expire is listed too, and is what is being
	// replaced. A new order is asked for again every so often, as the
	// install does, since one that fails is not retried.
	log.Info("waiting for the certificate (up to three minutes)")
	roots := CertRoots(st.Dir)
	deadline := time.Now().Add(3 * time.Minute)
	nextRetry := time.Now().Add(45 * time.Second)
	var last string
	for {
		s, err := ReadServed(ctx, o.MailHost, roots)
		if err == nil {
			if last = s.Problem(o.MailHost, time.Now()); last == "" {
				return s, nil
			}
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) {
			return s, fmt.Errorf("no certificate for %s after three minutes (%s): check that it resolves to this machine and that port 80 reaches it", o.MailHost, last)
		}
		if time.Now().After(nextRetry) {
			log.Info("asking again")
			_ = srv.RetryCertificates(ctx, domainID)
			nextRetry = time.Now().Add(45 * time.Second)
		}
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// CertsLogHint is where the server says why an order failed.
func CertsLogHint(st *state.State) string {
	if st.Shapes["server"] == "host" {
		return "journalctl -u " + HostServerUnit + " | grep -i acme"
	}
	return "docker compose --project-directory " + st.Dir + " logs server | grep -i acme"
}
