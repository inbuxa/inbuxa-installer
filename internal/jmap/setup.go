// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package jmap

// This file drives an inbuxa server's first boot over JMAP: bootstrap,
// certificates, the proxy's address, and the first account. It came from
// the webmail-oneshot and keeps its sequence, which SPEC.md 6.2 describes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Admin is the permanent administrator bootstrap provisions.
type Admin struct {
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

// CheckBootstrapMode confirms the server is fresh. x:Bootstrap exists only in
// bootstrap mode, so a server that has been set up before -- a volume left
// over from an earlier run, say -- refuses the call, and the tool stops before
// writing anything into someone's configured mail server.
func (c *Client) CheckBootstrapMode(ctx context.Context) error {
	_, err := c.Do(ctx, Call{"x:Bootstrap/get", map[string]any{"ids": []string{"singleton"}, "properties": []string{"id"}}, "0"})
	var me *MethodError
	var he *HTTPError
	// A configured server either refuses the method or, having no bootstrap
	// account any more, the credentials.
	if errors.As(err, &me) || (errors.As(err, &he) && he.Status == 401) {
		return fmt.Errorf("this server is not in bootstrap mode, so it has been configured before (%w)", err)
	}
	return err
}

// Bootstrap completes the setup wizard the web UI would otherwise walk someone
// through, and returns the administrator it creates. The temporary bootstrap
// account stops working once the server restarts out of bootstrap mode.
func (c *Client) Bootstrap(ctx context.Context, hostname, domain string) (Admin, error) {
	update := map[string]any{
		"serverHostname": hostname,
		"defaultDomain":  domain,
		// Off, and done explicitly afterwards (see EnableACME): on 0.16.22 this
		// flag creates no ACME provider and leaves the domain on manual
		// certificates, so turning it on would only look like it had worked.
		"requestTlsCertificate": false,
		"generateDkimKeys":      true,
		// The default logs to /var/log/stalwart, which does not exist in the
		// image and is not a volume. A container logs to stdout.
		"tracer": map[string]any{
			"@type": "Stdout", "enable": true, "level": "info",
			"ansi": false, "multiline": false, "lossy": false,
			"events": map[string]any{}, "eventsPolicy": "exclude",
		},
	}
	rs, err := c.Do(ctx, Call{"x:Bootstrap/set", map[string]any{"update": map[string]any{"singleton": update}}, "0"})
	if err != nil {
		return Admin{}, err
	}
	s, err := DecodeSet(rs[0])
	if err != nil {
		return Admin{}, err
	}
	var a Admin
	if err := json.Unmarshal(s.Updated["singleton"], &a); err != nil || a.Username == "" || a.Secret == "" {
		return Admin{}, errors.New("x:Bootstrap/set did not return the administrator it created")
	}
	return a, nil
}

// CreateAPIKey gives the signed-in account an API key and returns its id and
// the token, which the server shows only once. It expires on its own after
// ttl, so a setup that stops halfway does not leave a working key behind.
func (c *Client) CreateAPIKey(ctx context.Context, description string, ttl time.Duration) (id, token string, err error) {
	args := map[string]any{"create": map[string]any{
		"key": map[string]any{
			"description": description,
			"expiresAt":   time.Now().UTC().Add(ttl).Format(time.RFC3339),
			"permissions": map[string]any{"@type": "Inherit"},
		},
	}}
	rs, err := c.Do(ctx, Call{"x:ApiKey/set", args, "0"})
	if err != nil {
		return "", "", err
	}
	s, err := DecodeSet(rs[0])
	if err != nil {
		return "", "", err
	}
	var key struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(s.Created["key"], &key); err != nil || key.ID == "" || key.Secret == "" {
		return "", "", errors.New("x:ApiKey/set did not return the key it created")
	}
	return key.ID, key.Secret, nil
}

// DestroyAPIKey removes an API key from the signed-in account.
func (c *Client) DestroyAPIKey(ctx context.Context, id string) error {
	rs, err := c.Do(ctx, Call{"x:ApiKey/set", map[string]any{"destroy": []string{id}}, "0"})
	if err != nil {
		return err
	}
	_, err = DecodeSet(rs[0])
	return err
}

// DomainID finds a domain by name.
func (c *Client) DomainID(ctx context.Context, name string) (string, error) {
	rs, err := c.Do(ctx,
		Call{"x:Domain/query", map[string]any{}, "0"},
		Call{"x:Domain/get", map[string]any{
			"#ids":       map[string]any{"resultOf": "0", "name": "x:Domain/query", "path": "/ids"},
			"properties": []string{"name"},
		}, "1"},
	)
	if err != nil {
		return "", err
	}
	var got struct {
		List []struct{ ID, Name string } `json:"list"`
	}
	if err := json.Unmarshal(rs[1].Args, &got); err != nil {
		return "", err
	}
	for _, d := range got.List {
		if d.Name == name {
			return d.ID, nil
		}
	}
	return "", fmt.Errorf("domain %s does not exist on this server", name)
}

// EnableACME creates an ACME account using HTTP-01 and moves the domain's
// certificates onto it. the server starts the order at once, with no restart.
//
// HTTP-01 rather than the server's default TLS-ALPN-01, because Caddy holds 443.
// Caddy forwards /.well-known/acme-challenge/ on port 80 for the server's names
// to the server and uses TLS-ALPN-01 itself, so the two never compete.
func (c *Client) EnableACME(ctx context.Context, domainID, directory, contact string) (string, error) {
	provider := map[string]any{
		"challengeType": "Http01",
		"contact":       map[string]bool{contact: true},
		"renewBefore":   "R23",
		"maxRetries":    10,
		"reuseKey":      false,
	}
	if directory != "" {
		provider["directory"] = directory
	}
	rs, err := c.Do(ctx,
		Call{"x:AcmeProvider/set", map[string]any{"create": map[string]any{"acme": provider}}, "0"},
		Call{"x:Domain/set", map[string]any{"update": map[string]any{domainID: automaticCertificates("#acme")}}, "1"},
	)
	if err != nil {
		return "", err
	}
	s, err := DecodeSet(rs[0])
	if err != nil {
		return "", err
	}
	id, err := CreatedID(s, "acme")
	if err != nil {
		return "", fmt.Errorf("x:AcmeProvider/set: %w", err)
	}
	if _, err := DecodeSet(rs[1]); err != nil {
		return id, err
	}
	return id, nil
}

// RetryCertificates starts a fresh ACME order for the domain. A failed order
// is not retried on a restart; moving the domain to manual and straight back
// is what starts a new one.
func (c *Client) RetryCertificates(ctx context.Context, domainID string) error {
	var got struct {
		List []struct {
			CertificateManagement struct {
				Type           string `json:"@type"`
				AcmeProviderID string `json:"acmeProviderId"`
			} `json:"certificateManagement"`
		} `json:"list"`
	}
	rs, err := c.Do(ctx, Call{"x:Domain/get", map[string]any{"ids": []string{domainID}, "properties": []string{"certificateManagement"}}, "0"})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(rs[0].Args, &got); err != nil || len(got.List) != 1 {
		return errors.New("x:Domain/get did not return the domain")
	}
	cm := got.List[0].CertificateManagement
	if cm.Type != "Automatic" || cm.AcmeProviderID == "" {
		return fmt.Errorf("the domain's certificates are %q, not managed by ACME", cm.Type)
	}
	rs, err = c.Do(ctx,
		Call{"x:Domain/set", map[string]any{"update": map[string]any{domainID: map[string]any{"certificateManagement": map[string]any{"@type": "Manual"}}}}, "0"},
		Call{"x:Domain/set", map[string]any{"update": map[string]any{domainID: automaticCertificates(cm.AcmeProviderID)}}, "1"},
	)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if _, err := DecodeSet(r); err != nil {
			return err
		}
	}
	return nil
}

func automaticCertificates(providerID string) map[string]any {
	return map[string]any{"certificateManagement": map[string]any{
		"@type":          "Automatic",
		"acmeProviderId": providerID,
		// Empty is the server's default set: the mail host plus autoconfig,
		// autodiscover, mta-sts and ua-auto-config under the domain.
		"subjectAlternativeNames": map[string]bool{},
	}}
}

// Certificate is what the tool reports about an issued certificate.
type Certificate struct {
	Issuer                  string          `json:"issuer"`
	NotValidAfter           string          `json:"notValidAfter"`
	SubjectAlternativeNames map[string]bool `json:"subjectAlternativeNames"`
}

// Certificates lists the certificates the server holds.
func (c *Client) Certificates(ctx context.Context) ([]Certificate, error) {
	rs, err := c.Do(ctx,
		Call{"x:Certificate/query", map[string]any{}, "0"},
		Call{"x:Certificate/get", map[string]any{
			"#ids":       map[string]any{"resultOf": "0", "name": "x:Certificate/query", "path": "/ids"},
			"properties": []string{"issuer", "notValidAfter", "subjectAlternativeNames"},
		}, "1"},
	)
	if err != nil {
		return nil, err
	}
	var got struct {
		List []Certificate `json:"list"`
	}
	return got.List, json.Unmarshal(rs[1].Args, &got)
}

// TrustForwardedFor makes the server take a client's address from
// X-Forwarded-For. Its auto-ban works per address: behind Caddy, without this,
// one scanner probing for WordPress bans Caddy -- and with it every autoconfig
// lookup, DAV client and certificate renewal that comes through it. Seen on
// 0.16.22, as was the fix. It applies only once the server restarts.
//
// Safe here because nothing untrusted reaches the server's HTTP port: it is
// published on loopback only, and on the private network the only peers are
// Caddy, which sets the header itself, and the webmail, which sends none.
func (c *Client) TrustForwardedFor(ctx context.Context) error {
	rs, err := c.Do(ctx, Call{"x:Http/set", map[string]any{"update": map[string]any{"singleton": map[string]any{"useXForwarded": true}}}, "0"})
	if err != nil {
		return err
	}
	_, err = DecodeSet(rs[0])
	return err
}

// AllowIP exempts an address from the auto-ban. It applies only once the server
// restarts.
func (c *Client) AllowIP(ctx context.Context, address, reason string) error {
	rs, err := c.Do(ctx, Call{"x:AllowedIp/set", map[string]any{"create": map[string]any{
		"allow": map[string]any{"address": address, "reason": reason},
	}}, "0"})
	if err != nil {
		return err
	}
	s, err := DecodeSet(rs[0])
	if err != nil {
		return err
	}
	_, err = CreatedID(s, "allow")
	return err
}

// CreateUser creates an ordinary mailbox, in the shape the webmail's own
// Administration creates one.
func (c *Client) CreateUser(ctx context.Context, name, domainID, password string) (string, error) {
	rs, err := c.Do(ctx, Call{"x:Account/set", map[string]any{"create": map[string]any{
		"user": map[string]any{
			"@type":          "User",
			"name":           name,
			"domainId":       domainID,
			"description":    nil,
			"credentials":    map[string]any{"0": map[string]any{"@type": "Password", "secret": password}},
			"roles":          map[string]any{"@type": "User"},
			"permissions":    map[string]any{"@type": "Inherit"},
			"quotas":         map[string]any{},
			"aliases":        map[string]any{},
			"memberGroupIds": map[string]any{},
			// Required on create. Turning it on cannot be undone, which is not a
			// decision for a deploy tool to make on anyone's behalf.
			"encryptionAtRest": map[string]any{"@type": "Disabled"},
		},
	}}, "0"})
	if err != nil {
		return "", err
	}
	s, err := DecodeSet(rs[0])
	if err != nil {
		return "", err
	}
	return CreatedID(s, "user")
}

// DNSZone returns the records the server wants published for the domain, as a
// zone file fragment: MX, SPF, DKIM, DMARC, the SRV records, MTA-STS and the
// autoconfig names. It has no A or AAAA records; those depend on the host.
func (c *Client) DNSZone(ctx context.Context, domainID string) (string, error) {
	rs, err := c.Do(ctx, Call{"x:Domain/get", map[string]any{"ids": []string{domainID}, "properties": []string{"dnsZoneFile"}}, "0"})
	if err != nil {
		return "", err
	}
	var got struct {
		List []struct {
			DNSZoneFile string `json:"dnsZoneFile"`
		} `json:"list"`
	}
	if err := json.Unmarshal(rs[0].Args, &got); err != nil || len(got.List) != 1 {
		return "", errors.New("x:Domain/get did not return the domain")
	}
	return got.List[0].DNSZoneFile, nil
}
