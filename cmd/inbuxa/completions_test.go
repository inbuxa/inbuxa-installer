// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestModelMatchesSwitch guards against a top-level command being added to the
// dispatch in main() without a matching entry in the completion model (which
// would leave it out of the scripts and the man page), and the reverse. It
// reads the real `switch os.Args[1]` block, so the two cannot drift apart
// unnoticed.
func TestModelMatchesSwitch(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "switch os.Args[1] {") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("could not find the top-level switch in main.go")
	}
	caseRe := regexp.MustCompile(`"([^"]+)"`)
	inSwitch := map[string]bool{}
	for _, l := range lines[start+1:] {
		if l == "\t}" { // the switch's own closing brace, at one tab
			break
		}
		if !strings.HasPrefix(strings.TrimLeft(l, " "), "case ") && !strings.HasPrefix(l, "\tcase ") {
			continue
		}
		for _, m := range caseRe.FindAllStringSubmatch(l, -1) {
			tok := m[1]
			switch tok {
			case "-h", "--help": // these fold into "help"
			default:
				inSwitch[tok] = true
			}
		}
	}
	if len(inSwitch) == 0 {
		t.Fatal("parsed no cases from the top-level switch")
	}

	inModel := map[string]bool{}
	for _, c := range commands() {
		inModel[c.name] = true
	}
	for name := range inSwitch {
		if !inModel[name] {
			t.Errorf("command %q is dispatched in main() but missing from the completion model", name)
		}
	}
	for name := range inModel {
		if !inSwitch[name] {
			t.Errorf("command %q is in the completion model but not dispatched in main()", name)
		}
	}
}

func TestBashCompletion(t *testing.T) {
	s := bashCompletion()
	for _, want := range []string{
		"complete -F _inbuxa inbuxa",
		"install join survey", // the top-level list
		"--domain",            // an install flag
		"--token",             // a relay install flag
		"skip container host", // shape values
		"caddy snippets none", // proxy values
		"bash zsh fish",       // completions' own argument
	} {
		if !strings.Contains(s, want) {
			t.Errorf("bash completion is missing %q", want)
		}
	}
}

func TestZshCompletion(t *testing.T) {
	s := zshCompletion()
	for _, want := range []string{
		"#compdef inbuxa",
		"_describe",
		"compadd skip container host",
		"--exempt-address", // a join flag
		"--pg",             // a cluster doctor flag
	} {
		if !strings.Contains(s, want) {
			t.Errorf("zsh completion is missing %q", want)
		}
	}
	// A description's colon must be escaped so zsh does not split on it.
	if strings.Contains(zshDescribe("x", "a:b"), ":b") && !strings.Contains(zshDescribe("x", "a:b"), "a\\:b") {
		t.Errorf("zshDescribe did not escape the colon: %q", zshDescribe("x", "a:b"))
	}
}

func TestFishCompletion(t *testing.T) {
	s := fishCompletion()
	for _, want := range []string{
		"complete -c inbuxa -f",
		"__fish_use_subcommand -a install",
		"__fish_seen_subcommand_from relay",     // relay sub gating
		"-a 'install status uninstall'",         // relay subcommands
		"-l server -x -a 'skip container host'", // shape value hint
		"-l answers -r -F",                      // a file flag
	} {
		if !strings.Contains(s, want) {
			t.Errorf("fish completion is missing %q", want)
		}
	}
}

// Every command and flag in the model must appear somewhere in each script, so
// nothing the model knows about is silently dropped by a generator.
func TestEveryCommandAndFlagInEachScript(t *testing.T) {
	scripts := map[string]string{
		"bash": bashCompletion(),
		"zsh":  zshCompletion(),
		"fish": fishCompletion(),
	}
	for shell, s := range scripts {
		for _, c := range commands() {
			if !strings.Contains(s, c.name) {
				t.Errorf("%s completion is missing command %q", shell, c.name)
			}
			for _, fl := range c.flags {
				if !strings.Contains(s, strings.TrimLeft(flagToken(fl), "-")) {
					t.Errorf("%s completion is missing flag %q of %q", shell, fl, c.name)
				}
			}
			for _, sub := range c.subs {
				if !strings.Contains(s, sub.name) {
					t.Errorf("%s completion is missing %q subcommand %q", shell, c.name, sub.name)
				}
			}
		}
	}
}

func TestCompletionsCmdArgs(t *testing.T) {
	if code := completionsCmd(nil); code != 2 {
		t.Errorf("no shell should exit 2, got %d", code)
	}
	if code := completionsCmd([]string{"powershell"}); code != 2 {
		t.Errorf("an unsupported shell should exit 2, got %d", code)
	}
	for _, sh := range []string{"bash", "zsh", "fish"} {
		if code := completionsCmd([]string{sh}); code != 0 {
			t.Errorf("%s should exit 0, got %d", sh, code)
		}
	}
}
