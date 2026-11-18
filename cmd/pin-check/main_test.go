// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/pins"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/release"
)

// releasesFor serves a plausible set of releases for each component's repo: the
// pinned version plus, when bump is true, one newer candidate.
func releasesServer(t *testing.T, bump bool) *httptest.Server {
	t.Helper()
	newer := func(v string) string {
		// 27.1-rc.10 -> 27.1-rc.11; leaves a final pin alone.
		i := strings.LastIndex(v, ".")
		if i < 0 || !strings.Contains(v, "-rc.") {
			return v
		}
		return v[:i+1] + bumpNum(v[i+1:])
	}
	repoVersion := map[string]struct {
		prefix, current string
	}{
		"inbuxa-server":  {"v", release.ServerVersion},
		"inbuxa-admin":   {"v", release.ConsoleVersion},
		"inbuxa-webmail": {"inbuxa-v", release.WebmailVersion},
	}
	mux := http.NewServeMux()
	for repo, rv := range repoVersion {
		rv := rv
		mux.HandleFunc("/api/v1/repos/inbuxa/"+repo+"/releases", func(w http.ResponseWriter, r *http.Request) {
			rels := []giteaRelease{{TagName: rv.prefix + rv.current, Prerelease: true}}
			if bump {
				rels = append(rels, giteaRelease{TagName: rv.prefix + newer(rv.current), Prerelease: true})
			}
			// A draft and an unrelated tag, to prove both are ignored.
			rels = append(rels,
				giteaRelease{TagName: rv.prefix + "99.9-rc.9", Draft: true},
				giteaRelease{TagName: "unrelated", Prerelease: true},
			)
			_ = json.NewEncoder(w).Encode(rels)
		})
	}
	mux.HandleFunc("/v2/repositories/library/caddy/tags", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(caddyTags(bump, release.CaddyDigest))
	})
	return httptest.NewServer(mux)
}

// caddyTags is a page of Docker Hub's caddy tags: the pinned version's at
// digest, a newer patch when bump is true, and the moving and builder tags,
// which are not versions to move to.
func caddyTags(bump bool, digest string) map[string][]hubTag {
	tags := []hubTag{
		{Name: "alpine", Digest: "sha256:moving"},
		{Name: "2-alpine", Digest: "sha256:moving"},
		{Name: "2.11-alpine", Digest: "sha256:moving"},
		{Name: release.CaddyVersion + "-alpine", Digest: digest},
		{Name: "9.9.9-builder-alpine", Digest: "sha256:builder"},
		{Name: "2.99.0-beta.1-alpine", Digest: "sha256:beta"},
	}
	if bump {
		v, _ := pins.Parse(release.CaddyVersion)
		v.Patch++
		tags = append(tags, hubTag{Name: v.String() + "-alpine", Digest: "sha256:newer"})
	}
	return map[string][]hubTag{"results": tags}
}

func bumpNum(s string) string {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return fmt.Sprintf("%d", n+1)
}

func TestRunReportsAllCurrentWhenNothingIsNewer(t *testing.T) {
	srv := releasesServer(t, false)
	defer srv.Close()
	code, err := run(context.Background(), srv.Client(), srv.URL, srv.URL, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestRunExits10WhenAPinIsBehind(t *testing.T) {
	srv := releasesServer(t, true)
	defer srv.Close()
	code, err := run(context.Background(), srv.Client(), srv.URL, srv.URL, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if code != 10 {
		t.Errorf("exit code = %d, want 10", code)
	}
}

func TestRunErrorsOnABadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := run(context.Background(), srv.Client(), srv.URL, srv.URL, "", false); err == nil {
		t.Errorf("a 500 from the releases API should be an error")
	}
}

func TestFetchReleasesSkipsDraftsAndKeepsPrereleaseFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]giteaRelease{
			{TagName: "v27.1-rc.1", Prerelease: true},
			{TagName: "v27.1-rc.2", Draft: true},
			{TagName: "v27.1", Prerelease: false},
		})
	}))
	defer srv.Close()
	got, err := fetchReleases(context.Background(), srv.Client(), srv.URL, "", "inbuxa-server")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 non-draft releases, got %d: %+v", len(got), got)
	}
	if got[0].Tag != "v27.1-rc.1" || !got[0].Prerelease || got[1].Tag != "v27.1" || got[1].Prerelease {
		t.Errorf("releases not carried through faithfully: %+v", got)
	}
}

// Caddy is behind when Docker Hub has a newer patch of its series, or when
// its own version's tag now names another image; the moving tags, the
// builder images and betas are never a move.
func TestCheckCaddy(t *testing.T) {
	for _, c := range []struct {
		name   string
		bump   bool
		digest string
		stale  bool
		latest string
	}{
		{"current", false, release.CaddyDigest, false, ""},
		{"newer patch", true, release.CaddyDigest, true, ""},
		{"rebuilt", false, "sha256:rebuilt", true, release.CaddyVersion + " rebuilt, now sha256:rebuilt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(caddyTags(c.bump, c.digest))
			}))
			defer srv.Close()
			f, stale, err := checkCaddy(context.Background(), srv.Client(), srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			if stale != c.stale || (c.latest != "" && f.Latest != c.latest) {
				t.Errorf("stale = %v, finding %+v", stale, f)
			}
			if c.bump && (f.Latest == release.CaddyVersion || strings.Contains(f.Latest, "beta") || strings.HasPrefix(f.Latest, "9.")) {
				t.Errorf("moved to %s", f.Latest)
			}
		})
	}
}
