// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"reflect"
	"strings"
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/jmap"
)

func auto(id, name string, names ...string) jmap.DomainCert {
	return jmap.DomainCert{ID: id, Name: name, Kind: "Automatic", ProviderID: "acme1", Names: names}
}

// #82: the domain that orders a certificate naming the mail host.
func TestPlanOrder(t *testing.T) {
	install := auto("d1", "example.com")
	cases := []struct {
		name       string
		domains    []jmap.DomainCert
		host, srv  string
		want       orderPlan
		wantErrHas string
	}{
		{"same domain, default names, server already called that", []jmap.DomainCert{install}, "mail.example.com", "mail.example.com",
			orderPlan{Action: "retry", DomainID: "d1", DomainName: "example.com"}, ""},
		{"same domain, default names, server still has its old name", []jmap.DomainCert{install}, "mx.example.com", "mail.example.com",
			orderPlan{}, "set the hostname to mx.example.com"},
		{"another domain the server doesn't have: added for certificates", []jmap.DomainCert{install}, "mail.example.net", "",
			orderPlan{Action: "add", DomainName: "example.net", ProviderID: "acme1", Names: []string{"mail.example.net"}}, ""},
		{"another domain with manual certificates: the install's account, just that name",
			[]jmap.DomainCert{install, {ID: "d2", Name: "example.net", Kind: "Manual"}}, "mail.example.net", "",
			orderPlan{Action: "set", DomainID: "d2", DomainName: "example.net", ProviderID: "acme1", Names: []string{"mail.example.net"}}, ""},
		{"names entered in the console are kept, and the host added",
			[]jmap.DomainCert{install, auto("d2", "example.net", "autoconfig.example.net")}, "mail.example.net", "",
			orderPlan{Action: "set", DomainID: "d2", DomainName: "example.net", ProviderID: "acme1", Names: []string{"autoconfig.example.net", "mail.example.net"}}, ""},
		{"names that already include it: just order again",
			[]jmap.DomainCert{install, auto("d2", "example.net", "mail.example.net")}, "MAIL.example.net.", "",
			orderPlan{Action: "retry", DomainID: "d2", DomainName: "example.net"}, ""},
		{"the closest domain wins", []jmap.DomainCert{install, auto("d2", "example.net"), {ID: "d3", Name: "eu.example.net", Kind: "Manual"}},
			"mail.eu.example.net", "", orderPlan{Action: "set", DomainID: "d3", DomainName: "eu.example.net", ProviderID: "acme1", Names: []string{"mail.eu.example.net"}}, ""},
		{"install domain on manual certificates: turn ACME on, as before", []jmap.DomainCert{{ID: "d1", Name: "example.com", Kind: "Manual"}},
			"mail.example.com", "", orderPlan{Action: "enable", DomainID: "d1", DomainName: "example.com"}, ""},
		{"another domain with no ACME account anywhere", []jmap.DomainCert{{ID: "d1", Name: "example.com", Kind: "Manual"}},
			"mail.example.net", "", orderPlan{}, "no ACME account"},
		{"install domain missing", nil, "mail.example.com", "", orderPlan{}, "does not exist"},
	}
	for _, c := range cases {
		got, err := planOrder(c.domains, "example.com", c.host, c.srv)
		if c.wantErrHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErrHas) {
				t.Errorf("%s: err = %v, want one saying %q", c.name, err, c.wantErrHas)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v, %v\nwant %+v", c.name, got, err, c.want)
		}
	}
}
