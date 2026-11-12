// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pins decides whether the releases this installer pins
// (internal/release) have fallen behind the newest release of each component.
//
// A fresh install ships whatever release.go names, so a pin left behind is a
// fresh install shipped stale -- and, when the release it skipped fixed a
// vulnerability, shipped vulnerable. This package is the check that catches
// that: given the pinned version and the releases a repository has published,
// it reports the newer one to move to, or nothing when the pin is current.
//
// The channel follows the pin itself, and stays within the pin's own
// major.minor series. A release-candidate pin (27.1-rc.15) tracks the
// candidates and the final of that series: a newer 27.1 rc, or 27.1 final, is a
// move. A final pin (27.1) has nothing newer in its series. Crossing to another
// series -- a later flagship like 27.2, or the parallel calendar-versioned
// maintenance line (2026.11.11) the suite ships alongside the flagship -- is a
// deliberate change to a new installer line, not something this flags. This
// mirrors release.go's rule that an rc installer pins rcs and a final pins
// finals.
package pins

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed suite version: 27.1, 27.1.2, or 27.1-rc.15. A final
// release orders after every candidate of the same major.minor.patch.
type Version struct {
	Major int
	Minor int
	Patch int
	// RC is the candidate number; Final is true for a non-candidate release,
	// in which case RC is ignored.
	RC    int
	Final bool
}

// Parse reads a version like "27.1", "27.1.2", "27.1-rc.15", tolerating a
// leading "v". The component's own tag prefix (for example "inbuxa-v") must be
// stripped before calling.
func Parse(s string) (Version, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return Version{}, fmt.Errorf("empty version")
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Version{}, fmt.Errorf("version %q: want MAJOR.MINOR[.PATCH]", s)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("version %q: %q is not a number", s, p)
		}
		nums[i] = n
	}
	v := Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Final: !hasPre}
	if hasPre {
		rc, ok := strings.CutPrefix(pre, "rc.")
		if !ok {
			return Version{}, fmt.Errorf("version %q: only rc.N pre-releases are known, not %q", s, pre)
		}
		n, err := strconv.Atoi(rc)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("version %q: rc number %q is not a number", s, rc)
		}
		v.RC = n
	}
	return v, nil
}

// Compare orders two versions: negative if a is older, zero if equal, positive
// if a is newer. A final outranks any candidate of the same core.
func (a Version) Compare(b Version) int {
	if c := cmp(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmp(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmp(a.Patch, b.Patch); c != 0 {
		return c
	}
	switch {
	case a.Final && b.Final:
		return 0
	case a.Final:
		return 1
	case b.Final:
		return -1
	default:
		return cmp(a.RC, b.RC)
	}
}

func cmp(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// String renders the version the way release.go writes it.
func (a Version) String() string {
	core := fmt.Sprintf("%d.%d", a.Major, a.Minor)
	if a.Patch != 0 {
		core = fmt.Sprintf("%d.%d.%d", a.Major, a.Minor, a.Patch)
	}
	if a.Final {
		return core
	}
	return fmt.Sprintf("%s-rc.%d", core, a.RC)
}

// Release is one published release of a repository, as the check reads it from
// the releases API: its tag and whether it is a pre-release. Drafts are left
// out by the caller.
type Release struct {
	Tag        string
	Prerelease bool
}

// Component is one pinned thing to check: a name for the report, the current
// pinned version (from release.go, with no tag prefix), and the prefix the
// repository's tags carry (for example "v" or "inbuxa-v").
type Component struct {
	Name      string
	Current   string
	TagPrefix string
}

// Finding is a component whose pin is behind the newest release in its channel.
type Finding struct {
	Component string
	Current   string
	Latest    string
}

// Check compares a component's pin against the releases a repository has
// published and reports the newer version to move to, if any. The channel is
// taken from the current pin: a candidate pin also accepts the final of its
// core, a final pin accepts finals only.
func Check(c Component, releases []Release) (Finding, bool, error) {
	cur, err := Parse(c.Current)
	if err != nil {
		return Finding{}, false, fmt.Errorf("%s: current pin: %w", c.Name, err)
	}
	best := cur
	found := false
	for _, r := range releases {
		tag, ok := strings.CutPrefix(r.Tag, c.TagPrefix)
		if !ok {
			// A tag that does not carry this component's prefix is a release
			// of something else sharing the repo, or a differently-shaped tag;
			// it is not a candidate.
			continue
		}
		v, err := Parse(tag)
		if err != nil {
			// An unparseable tag is skipped, not fatal: one odd historical tag
			// must not blind the check to every good one.
			continue
		}
		// Stay in the pin's own series. A different major.minor is another
		// flagship (27.2) or the parallel calendar-versioned line (2026.x),
		// each a deliberate move to a new installer line, not a pin bump.
		if v.Major != cur.Major || v.Minor != cur.Minor {
			continue
		}
		// A final pin never moves onto a pre-release: not a candidate by its
		// tag, and not one flagged pre-release in the releases API.
		if cur.Final && (!v.Final || r.Prerelease) {
			continue
		}
		if v.Compare(best) > 0 {
			best = v
			found = true
		}
	}
	if !found {
		return Finding{}, false, nil
	}
	return Finding{Component: c.Name, Current: cur.String(), Latest: best.String()}, true, nil
}
