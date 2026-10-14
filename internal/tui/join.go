// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package tui

// Joining from the interface: the mail server skipped on the components
// screen means the console and the webmail are for a server that already
// runs on another machine, as `inbuxa join` installs them. One more screen
// asks which server and for an API key made in its console; leaving it asks
// the server, as join does before anything else, whether the key works and
// whether it can take these front ends. The rest is the same screens, the
// same plan.Options, and apply.Join instead of apply.Run.
//
// The key is typed, never shown, never written down and never put in the
// command the plan screen shows (that says --key -). It is dropped once the
// join has used it.

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

// recordedServer is how the mail server is installed on this machine, from
// its install record, or "". A machine that runs the server adds front ends
// with install, so the interface says so before a join gets anywhere, as
// apply.Join would refuse it after. Tests replace it.
var recordedServer = func() string {
	if st, err := state.Load(); err == nil {
		return st.Shapes["server"]
	}
	return ""
}

// joinState is the Join screen and the check it makes.
type joinState struct {
	fields []*field
	focus  int

	mu      sync.Mutex
	running bool
	done    bool
	err     error
	cancel  context.CancelFunc
	checked string // the address and choices the key was last checked against
}

const (
	joinAddress = iota
	joinKey
	joinReplace
)

func joinFields() []*field {
	return []*field{
		{label: "Server", hint: "the mail server's address, as a browser reaches it", placeholder: func(o plan.Options) string { return defaultServerURL(o) }},
		{label: "API key", hint: "made in that server's console: your account, API keys; used once, never kept", secret: true},
		{label: "Replace", hint: "put these in place of a console or webmail the server already lists", toggle: true},
	}
}

// joining is whether the screens describe a join: the server is elsewhere.
func (w *Wizard) joining() bool { return w.shapes[plan.Server] == plan.Skip }

// typing is whether a key is a letter for a text field rather than a command.
func (w *Wizard) typing() bool {
	switch w.screen {
	case screenNames:
		return !w.fields[w.focus].toggle
	case screenJoin:
		return !w.join.fields[w.join.focus].toggle
	}
	return false
}

// defaultServerURL is the server a join would reach if nothing else is
// typed: the mail server's name from the Names screen, over HTTPS.
func defaultServerURL(o plan.Options) string {
	switch {
	case o.MailHost != "":
		return "https://" + o.MailHost
	case o.Domain != "":
		return "https://mail." + o.Domain
	}
	return "https://mail.example.com"
}

// serverURL is the address typed, the way `inbuxa join` reads its argument.
func serverURL(typed string, o plan.Options) string {
	u := strings.TrimRight(strings.TrimSpace(typed), "/")
	if u == "" {
		u = defaultServerURL(o)
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		u = "https://" + u
	}
	return u
}

// joinOptions turns Options into a join's: the server's address and
// --replace, and no mail host of its own, as the join command builds them.
func (w *Wizard) joinOptions(o *plan.Options) {
	o.ServerURL = serverURL(w.join.fields[joinAddress].text(), *o)
	o.Replace = w.join.fields[joinReplace].text() == "yes"
	o.MailHost = ""
}

func (w *Wizard) joinKeyText() string { return strings.TrimSpace(w.join.fields[joinKey].text()) }

func (w *Wizard) updateJoin(k Key) {
	j := &w.join
	f := j.fields[j.focus]
	switch k.Kind {
	case KeyUp, KeyBackTab:
		if j.focus > 0 {
			j.focus--
		}
	case KeyDown, KeyTab:
		if j.focus < len(j.fields)-1 {
			j.focus++
		}
	case KeyEnter:
		if j.focus < len(j.fields)-1 {
			j.focus++
			return
		}
		if why := w.joinProblem(); why != "" {
			w.message = why
			return
		}
		w.startCheck()
	case KeyRune:
		if f.toggle {
			if k.Rune == ' ' || k.Rune == 'y' || k.Rune == 'n' {
				if (k.Rune == ' ' && f.text() != "yes") || k.Rune == 'y' {
					f.value = []rune("yes")
				} else {
					f.value = nil
				}
			}
			return
		}
		f.edit(k)
	default:
		f.edit(k)
	}
}

// joinProblem is why the Join screen can't be left yet, or "".
func (w *Wizard) joinProblem() string {
	o := w.Options()
	host := strings.TrimPrefix(strings.TrimPrefix(o.ServerURL, "https://"), "http://")
	host = strings.SplitN(host, "/", 2)[0]
	if host == "" || strings.ContainsAny(host, " @") {
		return fmt.Sprintf("%q is not a server address like https://mail.example.com.", o.ServerURL)
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if net.ParseIP(host) == nil && !strings.Contains(host, ".") {
		return fmt.Sprintf("%q is not a server address like https://mail.example.com.", o.ServerURL)
	}
	if w.joinKeyText() == "" {
		return "An API key is needed: make one in the server's console (your account, API keys) and type it here."
	}
	return ""
}

// startCheck asks the server, in the background, what `inbuxa join` asks
// before anything else. The screen says it is asking; Esc stops waiting.
func (w *Wizard) startCheck() {
	o := w.Options()
	key := w.joinKeyText()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	j := &w.join
	j.mu.Lock()
	j.running, j.done, j.err, j.cancel = true, false, nil, cancel
	j.mu.Unlock()
	w.message = "Asking " + o.ServerURL + " whether the key works…"
	go func() {
		err := w.Installer.JoinCheck(ctx, o, key)
		cancel()
		j.mu.Lock()
		if j.running { // not abandoned meanwhile
			j.running, j.done, j.err = false, true, err
			if err == nil {
				j.checked = checkedAs(o)
			}
		}
		j.mu.Unlock()
		w.poke()
	}()
}

// checkedAs is what a check vouched for: the server, and which front ends
// it was asked about. Changing any of it sends the operator back through
// the check before the plan.
func checkedAs(o plan.Options) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%t|%t", o.ServerURL, o.Shape(plan.Console), o.Shape(plan.Webmail), o.ConsoleHost, o.WebmailHost, o.Local, o.Replace)
}

func (j *joinState) checking() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running
}

// updateChecking is the keys while the server is being asked: Esc or Ctrl-C
// stops waiting, everything else waits.
func (w *Wizard) updateChecking(k Key) bool {
	if k.Kind == KeyEsc || k.Kind == KeyCtrlC {
		j := &w.join
		j.mu.Lock()
		if j.cancel != nil {
			j.cancel()
		}
		j.running = false
		j.mu.Unlock()
		w.message = "Stopped asking. Nothing has happened."
	}
	return false
}

// settle takes in a finished check: on to the next screen if the server
// agreed, the server's answer on screen if it didn't.
func (w *Wizard) settle() {
	j := &w.join
	j.mu.Lock()
	done, err := j.done, j.err
	j.done = false
	j.mu.Unlock()
	if !done {
		return
	}
	if err != nil {
		w.message = err.Error()
		return
	}
	if w.screen == screenJoin {
		w.message = "The key works, and the server can take these front ends."
		w.next()
	}
}

// joinChecked is whether the server agreed to exactly what the plan would do.
func (w *Wizard) joinChecked() bool {
	j := &w.join
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.checked != "" && j.checked == checkedAs(w.Options())
}

func (w *Wizard) viewJoin(width int) ([]string, string) {
	o := w.Options()
	body := []string{"  " + Bold("The server these front ends are for"), ""}
	for i, f := range w.join.fields {
		label := Pad(f.label, 20)
		var val string
		switch {
		case f.toggle:
			val = "[ ]"
			if f.text() == "yes" {
				val = "[x]"
			}
			if i == w.join.focus {
				val = Reverse(val)
			}
		case f.secret:
			// Never the key itself, only that something was typed.
			val = strings.Repeat("•", len(f.value))
			if len(f.value) == 0 && i != w.join.focus {
				val = Dim("none yet")
			}
			if i == w.join.focus {
				val += Reverse(" ")
			}
		case len(f.value) == 0 && i != w.join.focus:
			val = Dim(f.placeholder(o))
		case i == w.join.focus:
			s := f.value
			before, at, after := string(s[:f.cursor]), " ", ""
			if f.cursor < len(s) {
				at, after = string(s[f.cursor]), string(s[f.cursor+1:])
			}
			val = before + Reverse(at) + after
			if len(f.value) == 0 {
				val = Reverse(" ") + Dim(f.placeholder(o))
			}
		default:
			val = f.text()
		}
		line := "  "
		if i == w.join.focus {
			line = Accent("› ")
		}
		body = append(body, line+label+val)
		if i == w.join.focus {
			body = append(body, "    "+Dim(Pad("", 18)+f.hint))
		}
	}
	body = append(body, "")
	for _, l := range WrapProse("The server is reached only as a browser reaches it, over its own address. "+
		"The key goes to it once, to register these front ends; it isn't kept on this machine, "+
		"and you can remove it in the console when the join is done.", width-6) {
		body = append(body, "  "+Dim(l))
	}
	if w.join.checking() {
		return body, "Asking the server…   Esc: stop waiting"
	}
	return body, "↑↓ Tab: move   type to edit   Space: toggle   Enter: ask the server   Esc: back   Ctrl-C: quit"
}

// joinCommand is `inbuxa join` with the flags the screens chose. The key
// is read from standard input there, never written on a command line.
func joinCommand(o plan.Options) string {
	args := []string{"inbuxa", "join", o.ServerURL, "--key", "-"}
	add := func(flag, v, def string) {
		if v != "" && v != def {
			args = append(args, flag, v)
		}
	}
	if o.Local {
		args = append(args, "--local")
	}
	add("--domain", o.Domain, joinDomain(o.ServerURL))
	add("--console-host", o.ConsoleHost, "admin."+o.Domain)
	add("--webmail-host", o.WebmailHost, "webmail."+o.Domain)
	add("--email", o.ACMEEmail, "postmaster@"+o.Domain)
	add("--console", string(o.Shape(plan.Console)), "container")
	add("--webmail", string(o.Shape(plan.Webmail)), "container")
	if !o.Local {
		add("--proxy", o.Proxy, "caddy")
	}
	add("--dir", o.Dir, "/var/lib/inbuxa")
	if o.InstallDeps {
		args = append(args, "--install-deps")
	}
	if o.Replace {
		args = append(args, "--replace")
	}
	return strings.Join(append(args, "--yes"), " ")
}

// joinDomain is the domain `inbuxa join` takes from the server's address
// when --domain isn't given, so the command leaves it out when they agree.
func joinDomain(serverURL string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(serverURL, "https://"), "http://")
	host = strings.SplitN(strings.SplitN(host, "/", 2)[0], ":", 2)[0]
	if net.ParseIP(host) != nil {
		return ""
	}
	if _, rest, ok := strings.Cut(host, "."); ok && strings.Contains(rest, ".") {
		return rest
	}
	return host
}

// startJoin carries out the join, as `inbuxa join` does after its check.
func (w *Wizard) startJoin() {
	w.screen = screenApply
	w.message = ""
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	pr := &progress{poke: w.poke}
	w.progress = pr
	p := w.built
	key := w.joinKeyText()
	// The key leaves the screen's memory now; the join below has its copy.
	w.join.fields[joinKey].value, w.join.fields[joinKey].cursor = nil, 0
	go func() {
		res, err := w.Installer.Join(ctx, p, w.Facts, key, pr)
		var problems []string
		var extra []string
		if err == nil {
			pr.Step("checking it works")
			problems = w.Installer.JoinVerify(ctx, res, p.Options.Shape(plan.Console), p.Options.Shape(plan.Webmail), pr)
			extra = []string{
				"    joined to      " + p.Options.ServerURL,
				"",
				"  The API key is not kept here; you can remove it in the console now.",
			}
		}
		pr.mu.Lock()
		pr.done, pr.err, pr.result, pr.problems, pr.extra = true, err, res, problems, extra
		pr.mu.Unlock()
		pr.poke()
	}()
}
