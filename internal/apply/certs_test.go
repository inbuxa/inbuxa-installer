// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

type issued struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	der  []byte
}

// mint makes a certificate for names, signed by parent, or by itself when
// parent is nil.
func mint(t *testing.T, cn string, names []string, notAfter time.Time, ca bool, parent *issued) *issued {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"Lab"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		DNSNames:     names,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         ca, BasicConstraintsValid: true,
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &issued{c, key, der}
}

// serve presents leaf (and its chain) on a loopback port and returns the
// address.
func serve(t *testing.T, chain ...*issued) string {
	t.Helper()
	cert := tls.Certificate{PrivateKey: chain[0].key}
	for _, c := range chain {
		cert.Certificate = append(cert.Certificate, c.der)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { c.(*tls.Conn).Handshake(); c.Close() }(c)
		}
	}()
	return ln.Addr().String()
}

func TestTheCertificateOnTheMailPortsIsReadAsAClientSeesIt(t *testing.T) {
	ctx := context.Background()
	in := time.Now().Add(60 * 24 * time.Hour)
	root := mint(t, "Lab Root", nil, in.Add(time.Hour), true, nil)
	leaf := mint(t, "mx.lab.test", []string{"mx.lab.test", "autoconfig.lab.test"}, in, false, root)
	roots := x509.NewCertPool()
	roots.AddCert(root.cert)

	s, err := ServedCertificate(ctx, serve(t, leaf), "mx.lab.test", roots)
	if err != nil {
		t.Fatal(err)
	}
	if p := s.Problem("mx.lab.test", time.Now()); p != "" {
		t.Errorf("a trusted certificate for the name, two months out, is a problem: %s", p)
	}
	if s.SelfSigned || s.Issuer != "Lab Root" || len(s.Names) != 2 {
		t.Errorf("read back as %+v", s)
	}

	// The same certificate without its root trusted.
	s, _ = ServedCertificate(ctx, serve(t, leaf), "mx.lab.test", x509.NewCertPool())
	if p := s.Problem("mx.lab.test", time.Now()); !strings.Contains(p, "does not verify") {
		t.Errorf("an untrusted issuer reads %q", p)
	}

	// The server's own, before any order succeeded.
	self := mint(t, "mx.lab.test", []string{"mx.lab.test"}, in, false, nil)
	s, _ = ServedCertificate(ctx, serve(t, self), "mx.lab.test", roots)
	if p := s.Problem("mx.lab.test", time.Now()); !strings.Contains(p, "self-signed") {
		t.Errorf("a self-signed certificate reads %q", p)
	}
}

func TestWhatIsWrongWithACertificateIsSaidInOnePhrase(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name string
		s    Served
		want string
	}{
		{"fine", Served{NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(60 * 24 * time.Hour), Names: []string{"mx.lab.test"}}, ""},
		{"wildcard", Served{NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(60 * 24 * time.Hour), Names: []string{"*.lab.test"}}, ""},
		{"expired", Served{NotAfter: now.Add(-time.Hour), Names: []string{"mx.lab.test"}}, "expired"},
		{"other name", Served{NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(60 * 24 * time.Hour), Names: []string{"mail.lab.test"}}, "does not name mx.lab.test"},
		{"renewal stuck", Served{NotBefore: now.Add(-85 * 24 * time.Hour), NotAfter: now.Add(5 * 24 * time.Hour), Names: []string{"mx.lab.test"}}, "in 5 days and has not been renewed"},
		{"short-lived, new", Served{NotBefore: now.Add(-time.Hour), NotAfter: now.Add(5 * 24 * time.Hour), Names: []string{"mx.lab.test"}}, ""},
		{"short-lived, stuck", Served{NotBefore: now.Add(-110 * time.Hour), NotAfter: now.Add(10 * time.Hour), Names: []string{"mx.lab.test"}}, "in 10 hours"},
	} {
		got := c.s.Problem("mx.lab.test", now)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, wanted %q", c.name, got, c.want)
		}
	}
}

func TestTheMailHostComesFromWhatIsKnownBeforeTheDefault(t *testing.T) {
	st := &state.State{Domain: "lab.test"}
	if h := CertsMailHost("", st, ""); h != "mail.lab.test" {
		t.Errorf("nothing known: %s", h)
	}
	if h := CertsMailHost("", st, "mx.lab.test"); h != "mx.lab.test" {
		t.Errorf("from the deployment: %s", h)
	}
	st.MailHost = "smtp.lab.test"
	if h := CertsMailHost("", st, "mx.lab.test"); h != "smtp.lab.test" {
		t.Errorf("recorded: %s", h)
	}
	if h := CertsMailHost("given.lab.test", st, "mx.lab.test"); h != "given.lab.test" {
		t.Errorf("given: %s", h)
	}
}

func TestALocalInstallIsKnownEvenIfItWasNotRecorded(t *testing.T) {
	dir := t.TempDir()
	st := &state.State{Dir: dir}
	if LocalInstall(st) {
		t.Fatal("an empty directory is local")
	}
	os.WriteFile(filepath.Join(dir, "server.env"), []byte("INBUXA_PUBLIC_URL=http://localhost:8081\n"), 0o600)
	if !LocalInstall(st) {
		t.Error("a server told its own loopback address is not local")
	}
	if !LocalInstall(&state.State{Dir: t.TempDir(), Local: true}) {
		t.Error("a recorded --local is not local")
	}
}
