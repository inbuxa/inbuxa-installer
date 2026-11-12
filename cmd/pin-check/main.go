// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command pin-check reports whether the releases this installer pins
// (internal/release) have fallen behind the newest release of each component.
//
// It reads each repository's releases from the Gitea API and compares the
// newest one in the pin's channel against the pin. It prints a line per
// component and exits 10 when any pin is behind, 0 when all are current, and 1
// on an error -- so a scheduled job turns a stale pin into a red run (and, with
// --issue, an open issue) instead of a fresh install shipped stale.
//
// It only reads; it never edits release.go. The move itself stays a reviewed
// change: the report names the version to move to, and release.go's checksums
// are still taken from each release's signed SHA256SUMS by hand.
//
// Usage:
//
//	pin-check [--base-url URL] [--timeout D] [--issue]
//
// GITEA_TOKEN, if set, authenticates the API reads (needed while the
// repositories are private). --issue opens or updates a tracking issue in this
// repository when something is stale, and needs a token with issue write.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/pins"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/release"
)

// component ties a pin from internal/release to the repository whose releases
// name its newer versions.
type component struct {
	pins.Component
	repo string
}

func components() []component {
	return []component{
		{pins.Component{Name: "inbuxa-server", Current: release.ServerVersion, TagPrefix: "v"}, "inbuxa-server"},
		{pins.Component{Name: "inbuxa-admin (console)", Current: release.ConsoleVersion, TagPrefix: "v"}, "inbuxa-admin"},
		{pins.Component{Name: "inbuxa-webmail", Current: release.WebmailVersion, TagPrefix: "inbuxa-v"}, "inbuxa-webmail"},
	}
}

func main() {
	baseURL := flag.String("base-url", "https://git.coffeylabs.org", "the Gitea base URL whose releases to read")
	timeout := flag.Duration("timeout", 30*time.Second, "overall deadline for the API reads")
	openIssue := flag.Bool("issue", false, "open or update a tracking issue in this repo when a pin is stale")
	flag.Parse()

	token := strings.TrimSpace(os.Getenv("GITEA_TOKEN"))
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	code, err := run(ctx, http.DefaultClient, *baseURL, token, *openIssue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pin-check: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(ctx context.Context, client *http.Client, baseURL, token string, openIssue bool) (int, error) {
	var findings []pins.Finding
	for _, c := range components() {
		rels, err := fetchReleases(ctx, client, baseURL, token, c.repo)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", c.repo, err)
		}
		f, stale, err := pins.Check(c.Component, rels)
		if err != nil {
			return 0, err
		}
		if stale {
			findings = append(findings, f)
			fmt.Printf("STALE  %-24s %s -> %s\n", c.Name, f.Current, f.Latest)
		} else {
			fmt.Printf("ok     %-24s %s\n", c.Name, c.Current)
		}
	}

	if len(findings) == 0 {
		fmt.Println("\nAll pins are current.")
		return 0, nil
	}

	fmt.Printf("\n%d pin(s) behind. Move release.go to the versions above, taking each\n", len(findings))
	fmt.Println("checksum from that release's signed SHA256SUMS, and open a pull request.")
	if openIssue {
		if token == "" {
			return 0, fmt.Errorf("--issue needs GITEA_TOKEN")
		}
		if err := ensureIssue(ctx, client, baseURL, token, findings); err != nil {
			return 0, fmt.Errorf("opening the tracking issue: %w", err)
		}
	}
	return 10, nil
}

// giteaRelease is the slice of a Gitea release the check needs.
type giteaRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func fetchReleases(ctx context.Context, client *http.Client, baseURL, token, repo string) ([]pins.Release, error) {
	u := fmt.Sprintf("%s/api/v1/repos/inbuxa/%s/releases?limit=50", strings.TrimRight(baseURL, "/"), repo)
	body, err := apiGet(ctx, client, u, token)
	if err != nil {
		return nil, err
	}
	var got []giteaRelease
	if err := json.Unmarshal(body, &got); err != nil {
		return nil, fmt.Errorf("decoding releases: %w", err)
	}
	out := make([]pins.Release, 0, len(got))
	for _, r := range got {
		if r.Draft {
			continue
		}
		out = append(out, pins.Release{Tag: r.TagName, Prerelease: r.Prerelease})
	}
	return out, nil
}

func apiGet(ctx context.Context, client *http.Client, u, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return body, nil
}

const issueTitle = "Pinned releases are behind upstream"

// ensureIssue opens the tracking issue, or updates the open one, so a stale pin
// is visible without flooding: one issue, its body rewritten to the current
// findings, reused until it is closed.
func ensureIssue(ctx context.Context, client *http.Client, baseURL, token string, findings []pins.Finding) error {
	body := issueBody(findings)
	existing, err := openIssueNumber(ctx, client, baseURL, token)
	if err != nil {
		return err
	}
	base := strings.TrimRight(baseURL, "/")
	if existing > 0 {
		u := fmt.Sprintf("%s/api/v1/repos/inbuxa/inbuxa-installer/issues/%d", base, existing)
		return apiPatch(ctx, client, u, token, map[string]string{"body": body})
	}
	u := fmt.Sprintf("%s/api/v1/repos/inbuxa/inbuxa-installer/issues", base)
	return apiPost(ctx, client, u, token, map[string]string{"title": issueTitle, "body": body})
}

func issueBody(findings []pins.Finding) string {
	var b strings.Builder
	b.WriteString("These default-release pins in `internal/release/release.go` are behind the newest release in their channel:\n\n")
	for _, f := range findings {
		fmt.Fprintf(&b, "- **%s**: `%s` -> `%s`\n", f.Component, f.Current, f.Latest)
	}
	b.WriteString("\nMove each pin, taking its checksum from that release's signed `SHA256SUMS`, and open a pull request. This issue is maintained by `pin-check`; it updates itself and can be closed once the pins are current.\n")
	return b.String()
}

func openIssueNumber(ctx context.Context, client *http.Client, baseURL, token string) (int, error) {
	u := fmt.Sprintf("%s/api/v1/repos/inbuxa/inbuxa-installer/issues?state=open&type=issues&limit=50",
		strings.TrimRight(baseURL, "/"))
	body, err := apiGet(ctx, client, u, token)
	if err != nil {
		return 0, err
	}
	var issues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal(body, &issues); err != nil {
		return 0, fmt.Errorf("decoding issues: %w", err)
	}
	for _, is := range issues {
		if is.Title == issueTitle {
			return is.Number, nil
		}
	}
	return 0, nil
}

func apiPost(ctx context.Context, client *http.Client, u, token string, payload map[string]string) error {
	return apiWrite(ctx, client, http.MethodPost, u, token, payload, http.StatusCreated)
}

func apiPatch(ctx context.Context, client *http.Client, u, token string, payload map[string]string) error {
	return apiWrite(ctx, client, http.MethodPatch, u, token, payload, http.StatusOK)
}

func apiWrite(ctx context.Context, client *http.Client, method, u, token string, payload map[string]string, want int) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, strings.NewReader(string(buf)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s: %s", method, u, resp.Status)
	}
	return nil
}
