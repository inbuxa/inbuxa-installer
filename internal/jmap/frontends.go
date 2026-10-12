// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package jmap

// inbuxa:FrontEnds (contract C-4): where a server's console and webmail are.
// Setting it registers their OAuth clients and allows their origins on every
// node, which is all a front end on another machine needs from the server --
// and why `inbuxa join` needs nothing from that machine but an API key.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

var usingInbuxa = []string{"urn:ietf:params:jmap:core", "urn:inbuxa:jmap"}

// FrontEnds is the server's record of its front ends, and what its
// environment says beside it.
type FrontEnds struct {
	AdminURL                       *string  `json:"adminUrl"`
	WebmailURL                     *string  `json:"webmailUrl"`
	WebmailClientSecretSet         bool     `json:"webmailClientSecretSet"`
	ExtraOrigins                   []string `json:"extraOrigins"`
	EnvironmentAdminURL            *string  `json:"environmentAdminUrl"`
	EnvironmentWebmailURL          *string  `json:"environmentWebmailUrl"`
	EnvironmentWebmailClientSecret bool     `json:"environmentWebmailClientSecret"`
}

// ErrNoFrontEnds is a server too old to have inbuxa:FrontEnds.
var ErrNoFrontEnds = errors.New("this server has no inbuxa:FrontEnds; it needs a release newer than 2026.10.12")

// AccountID is the signed-in account's id, which inbuxa: methods name.
func (c *Client) AccountID(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/jmap/session", nil)
	if err != nil {
		return "", err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	} else {
		req.SetBasicAuth(c.Username, c.Password)
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", &HTTPError{Status: res.StatusCode, Body: http.StatusText(res.StatusCode)}
	}
	var session struct {
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		PrimaryAccounts map[string]string          `json:"primaryAccounts"`
	}
	if err := json.NewDecoder(res.Body).Decode(&session); err != nil {
		return "", fmt.Errorf("not a JMAP session: %w", err)
	}
	if _, ok := session.Capabilities["urn:inbuxa:jmap"]; !ok {
		return "", errors.New("this is not an inbuxa server: its session has no urn:inbuxa:jmap")
	}
	for _, capability := range []string{"urn:inbuxa:jmap", "urn:ietf:params:jmap:core"} {
		if id := session.PrimaryAccounts[capability]; id != "" {
			return id, nil
		}
	}
	for _, id := range session.PrimaryAccounts {
		return id, nil
	}
	return "", errors.New("the session names no account")
}

// GetFrontEnds reads inbuxa:FrontEnds.
func (c *Client) GetFrontEnds(ctx context.Context, accountID string) (FrontEnds, error) {
	var fe FrontEnds
	rs, err := c.do(ctx, usingInbuxa, Call{"inbuxa:FrontEnds/get", map[string]any{"accountId": accountID}, "0"})
	var me *MethodError
	if errors.As(err, &me) && me.Type == "unknownMethod" {
		return fe, ErrNoFrontEnds
	}
	if err != nil {
		return fe, err
	}
	var got struct {
		List []FrontEnds `json:"list"`
	}
	if err := json.Unmarshal(rs[0].Args, &got); err != nil {
		return fe, err
	}
	if len(got.List) != 1 {
		return fe, errors.New("inbuxa:FrontEnds/get returned nothing: is this account a server administrator?")
	}
	return got.List[0], nil
}

// SetFrontEnds updates inbuxa:FrontEnds with the properties given; null
// clears one.
func (c *Client) SetFrontEnds(ctx context.Context, accountID string, update map[string]any) error {
	rs, err := c.do(ctx, usingInbuxa, Call{"inbuxa:FrontEnds/set", map[string]any{
		"accountId": accountID,
		"update":    map[string]any{"singleton": update},
	}, "0"})
	if err != nil {
		return err
	}
	_, err = DecodeSet(rs[0])
	return err
}
