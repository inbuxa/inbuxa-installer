// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"strings"
)

// The command model below is the one source the completion scripts and the
// man page are generated from, so a new command or flag reaches both at once.
// It mirrors the flags each command's FlagSet defines in this file;
// completions_test.go checks the top-level list against the usage text, which
// catches a command added without a model entry.

type command struct {
	name  string
	desc  string    // one line, for the shell's menu and the man page
	arg   string    // a positional the command takes: "url", "component", "shell"; "" for none
	flags []string  // long flag names, without the leading dashes
	subs  []command // subcommands, for relay and cluster
}

// commands is every top-level command, in the order the usage text lists them.
func commands() []command {
	install := []string{
		"domain", "mail-host", "console-host", "webmail-host", "email",
		"server", "console", "webmail", "proxy", "dir", "local",
		"acme-directory", "acme-ca-root", "install-deps", "key",
		"dry-run", "yes", "answers",
	}
	join := []string{
		"key", "console", "webmail", "domain", "console-host", "webmail-host",
		"email", "proxy", "dir", "local", "install-deps", "replace",
		"exempt-address", "acme-directory", "acme-ca-root", "dry-run", "yes",
	}
	return []command{
		{name: "install", desc: "Install or converge the suite on this machine", flags: install},
		{name: "join", desc: "Install the console and webmail here for a server at URL", arg: "url", flags: join},
		{name: "survey", desc: "Report what this machine is, as the installer sees it"},
		{name: "deps", desc: "Report what a shape needs, and with --install fix it", flags: []string{"server", "console", "webmail", "install"}},
		{name: "plan", desc: "Show what a topology file would change here", flags: []string{"f", "machine", "yes", "install-deps", "key"}},
		{name: "apply", desc: "Make this machine match a topology file", flags: []string{"f", "machine", "yes", "install-deps", "key"}},
		{name: "export", desc: "Write a topology file from what is here", flags: []string{"o"}},
		{name: "status", desc: "Report what is installed here, and whether it agrees"},
		{name: "doctor", desc: "Run a read-only health check of this install", flags: []string{"timeout"}},
		{name: "backup", desc: "Archive this machine's mail server to <dir>/backups", flags: []string{"yes"}},
		{name: "upgrade", desc: "Move what is here to the releases this installer installs", arg: "component", flags: []string{"dry-run", "yes", "no-backup", "check"}},
		{name: "uninstall", desc: "Remove the programs, keep the data", flags: []string{"dry-run", "yes", "purge"}},
		{name: "certs", desc: "Inspect or renew the mail server's certificate", flags: []string{"key", "mail-host", "email", "acme-directory", "dry-run"}},
		{name: "dns", desc: "Print the DNS records the domain needs", flags: []string{"refresh", "key", "domain"}},
		{name: "relay", desc: "Manage inbuxa Relay on a machine of its own", subs: []command{
			{name: "install", desc: "Install the relay here", flags: []string{"token", "dry-run", "yes"}},
			{name: "status", desc: "Report the relay's status (needs root)"},
			{name: "uninstall", desc: "Remove the relay", flags: []string{"purge", "discard-held", "dry-run", "yes"}},
		}},
		{name: "cluster", desc: "Join a cluster's data layer, or check it can be reached", subs: []command{
			{name: "doctor", desc: "Check this node can reach a cluster's data layer", flags: []string{"pg", "nats", "etcd", "patroni", "s3", "timeout"}},
			{name: "join", desc: "Point this node's server at a cluster's shared store", arg: "file", flags: []string{"f", "machine", "dry-run", "yes"}},
		}},
		{name: "version", desc: "Print this program's version"},
		{name: "completions", desc: "Output a shell completion script (bash, zsh or fish)", arg: "shell"},
		{name: "man", desc: "Output the manual page, in roff"},
		{name: "help", desc: "Show the help"},
	}
}

// Flags whose value is one of a small fixed set, or a file, so the scripts can
// offer the right thing after the flag.
var (
	shapeFlags = map[string]bool{"server": true, "console": true, "webmail": true}
	proxyFlag  = "proxy"
	fileFlags  = map[string]bool{"f": true, "o": true, "answers": true, "acme-ca-root": true, "dir": true}
)

const (
	shapeValues = "skip container host"
	proxyValues = "caddy snippets none"
	shellValues = "bash zsh fish"
)

// flagToken renders a flag as it is typed: -f for a one-letter flag, --name
// otherwise. Go's flag package accepts either form, but this matches the docs.
func flagToken(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

func flagTokens(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = flagToken(n)
	}
	return out
}

// completionsCmd writes a shell completion script to standard output. The
// shell is given as the one argument; the script is generated from the command
// model so it stays in step with the commands and flags.
func completionsCmd(args []string) int {
	if len(args) != 1 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, "usage: inbuxa completions bash|zsh|fish")
		fmt.Fprintln(os.Stderr, "\nInstall, for the current shell, with e.g.:")
		fmt.Fprintln(os.Stderr, "  inbuxa completions bash | sudo tee /etc/bash_completion.d/inbuxa")
		fmt.Fprintln(os.Stderr, "  inbuxa completions zsh  > \"${fpath[1]}/_inbuxa\"")
		fmt.Fprintln(os.Stderr, "  inbuxa completions fish > ~/.config/fish/completions/inbuxa.fish")
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
			return 0
		}
		return 2
	}
	var s string
	switch args[0] {
	case "bash":
		s = bashCompletion()
	case "zsh":
		s = zshCompletion()
	case "fish":
		s = fishCompletion()
	default:
		fmt.Fprintf(os.Stderr, "inbuxa has no completions for %q: bash, zsh or fish\n", args[0])
		return 2
	}
	fmt.Print(s)
	return 0
}

func topNames() []string {
	cs := commands()
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.name
	}
	return out
}

// ---- bash ----

func bashCompletion() string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	p("# bash completion for inbuxa -- generated by `inbuxa completions bash`")
	p("_inbuxa() {")
	p("    local cur prev cmd sub i")
	p("    cur=\"${COMP_WORDS[COMP_CWORD]}\"")
	p("    prev=\"${COMP_WORDS[COMP_CWORD-1]}\"")
	p("")
	p("    # a value for the flag just typed")
	p("    case \"$prev\" in")
	p("        --server|--console|--webmail) COMPREPLY=( $(compgen -W %q -- \"$cur\") ); return;;", shapeValues)
	p("        --proxy) COMPREPLY=( $(compgen -W %q -- \"$cur\") ); return;;", proxyValues)
	p("        -f|-o|--answers|--acme-ca-root|--dir) COMPREPLY=( $(compgen -f -- \"$cur\") ); return;;")
	p("    esac")
	p("")
	p("    # the first word that is not a flag is the command")
	p("    cmd=\"\"")
	p("    for (( i=1; i<COMP_CWORD; i++ )); do")
	p("        case \"${COMP_WORDS[i]}\" in -*) ;; *) cmd=\"${COMP_WORDS[i]}\"; break;; esac")
	p("    done")
	p("")
	p("    if [ -z \"$cmd\" ]; then")
	p("        COMPREPLY=( $(compgen -W %q -- \"$cur\") ); return", strings.Join(topNames(), " "))
	p("    fi")
	p("")
	p("    case \"$cmd\" in")
	for _, c := range commands() {
		if len(c.subs) > 0 {
			p("        %s)", c.name)
			p("            sub=\"\"")
			p("            for (( i=2; i<COMP_CWORD; i++ )); do case \"${COMP_WORDS[i]}\" in -*) ;; *) sub=\"${COMP_WORDS[i]}\"; break;; esac; done")
			p("            case \"$sub\" in")
			for _, s := range c.subs {
				if len(s.flags) == 0 {
					p("                %s) COMPREPLY=() ;;", s.name)
				} else {
					p("                %s) COMPREPLY=( $(compgen -W %q -- \"$cur\") ) ;;", s.name, strings.Join(flagTokens(s.flags), " "))
				}
			}
			p("                *) COMPREPLY=( $(compgen -W %q -- \"$cur\") ) ;;", subNames(c))
			p("            esac ;;")
			continue
		}
		words := strings.Join(flagTokens(c.flags), " ")
		if c.arg == "shell" {
			words = shellValues // completions takes a shell name, not flags
		}
		if words == "" {
			p("        %s) COMPREPLY=() ;;", c.name)
		} else {
			p("        %s) COMPREPLY=( $(compgen -W %q -- \"$cur\") ) ;;", c.name, words)
		}
	}
	p("    esac")
	p("}")
	p("complete -F _inbuxa inbuxa")
	return b.String()
}

func subNames(c command) string {
	out := make([]string, len(c.subs))
	for i, s := range c.subs {
		out[i] = s.name
	}
	return strings.Join(out, " ")
}

// ---- zsh ----

func zshCompletion() string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	p("#compdef inbuxa")
	p("# zsh completion for inbuxa -- generated by `inbuxa completions zsh`")
	p("_inbuxa() {")
	p("    local cmd sub i")
	p("    local -a top")
	p("    top=(")
	for _, c := range commands() {
		p("        %s", zshDescribe(c.name, c.desc))
	}
	p("    )")
	p("")
	p("    case \"${words[CURRENT-1]}\" in")
	p("        --server|--console|--webmail) compadd skip container host; return;;")
	p("        --proxy) compadd caddy snippets none; return;;")
	p("        -f|-o|--answers|--acme-ca-root|--dir) _files; return;;")
	p("    esac")
	p("")
	p("    cmd=\"\"")
	p("    for (( i=2; i<CURRENT; i++ )); do")
	p("        case \"${words[i]}\" in -*) ;; *) cmd=\"${words[i]}\"; break;; esac")
	p("    done")
	p("")
	p("    if [[ -z \"$cmd\" ]]; then _describe -t commands 'inbuxa command' top; return; fi")
	p("")
	p("    case \"$cmd\" in")
	for _, c := range commands() {
		if len(c.subs) > 0 {
			p("        %s)", c.name)
			p("            local -a subs")
			p("            subs=(")
			for _, s := range c.subs {
				p("                %s", zshDescribe(s.name, s.desc))
			}
			p("            )")
			p("            sub=\"\"")
			p("            for (( i=3; i<=CURRENT; i++ )); do case \"${words[i]}\" in -*) ;; \"\") ;; *) sub=\"${words[i]}\"; break;; esac; done")
			p("            case \"$sub\" in")
			for _, s := range c.subs {
				if len(s.flags) == 0 {
					p("                %s) ;;", s.name)
				} else {
					p("                %s) compadd -- %s ;;", s.name, strings.Join(flagTokens(s.flags), " "))
				}
			}
			p("                *) _describe -t commands '%s command' subs ;;", c.name)
			p("            esac ;;")
			continue
		}
		if c.arg == "shell" {
			p("        %s) compadd bash zsh fish ;;", c.name)
			continue
		}
		if len(c.flags) == 0 {
			p("        %s) ;;", c.name)
		} else {
			p("        %s) compadd -- %s ;;", c.name, strings.Join(flagTokens(c.flags), " "))
		}
	}
	p("    esac")
	p("}")
	p("_inbuxa \"$@\"")
	return b.String()
}

// zshDescribe renders one 'name:description' entry for _describe. The colon in
// the description is escaped so zsh does not read it as the separator, and the
// whole is single-quoted with any apostrophe escaped the POSIX way ('\”) so a
// word like "machine's" does not end the quote.
func zshDescribe(name, desc string) string {
	body := name + ":" + strings.ReplaceAll(desc, ":", "\\:")
	return "'" + strings.ReplaceAll(body, "'", `'\''`) + "'"
}

// ---- fish ----

func fishCompletion() string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	p("# fish completion for inbuxa -- generated by `inbuxa completions fish`")
	p("complete -c inbuxa -f")
	p("")
	p("# top-level commands")
	for _, c := range commands() {
		p("complete -c inbuxa -n __fish_use_subcommand -a %s -d %s", c.name, fishQuote(c.desc))
	}
	for _, c := range commands() {
		if len(c.subs) > 0 {
			p("")
			p("# %s subcommands", c.name)
			p("complete -c inbuxa -n '__fish_seen_subcommand_from %s; and not __fish_seen_subcommand_from %s' -a '%s'", c.name, subNames(c), subNames(c))
			for _, s := range c.subs {
				for _, fl := range s.flags {
					p("complete -c inbuxa -n '__fish_seen_subcommand_from %s; and __fish_seen_subcommand_from %s' %s", c.name, s.name, fishFlag(fl))
				}
			}
			continue
		}
		if c.arg == "shell" {
			p("complete -c inbuxa -n '__fish_seen_subcommand_from %s' -a 'bash zsh fish'", c.name)
			continue
		}
		if len(c.flags) == 0 {
			continue
		}
		p("")
		p("# %s flags", c.name)
		for _, fl := range c.flags {
			p("complete -c inbuxa -n '__fish_seen_subcommand_from %s' %s", c.name, fishFlag(fl))
		}
	}
	return b.String()
}

// fishFlag renders one flag for `complete`, with a value hint where the value
// is a known set or a file.
func fishFlag(name string) string {
	var spec string
	if len(name) == 1 {
		spec = "-s " + name
	} else {
		spec = "-l " + name
	}
	switch {
	case shapeFlags[name]:
		return spec + " -x -a 'skip container host'"
	case name == proxyFlag:
		return spec + " -x -a 'caddy snippets none'"
	case fileFlags[name]:
		return spec + " -r -F"
	}
	return spec
}

func fishQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}
