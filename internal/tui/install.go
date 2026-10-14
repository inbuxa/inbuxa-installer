// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package tui

// The installer's interface: six screens, always in this order, with the
// plan in front of the operator before anything happens (installer spec §4).
// With the mail server skipped, it joins a server on another machine instead,
// and a seventh screen, Join, asks for that server and an API key (join.go).
//
// It is a way of filling in the flags with the machine's own facts on
// screen, not a second implementation. Every screen writes into the same
// plan.Options the flags do, the plan is plan.Build's, the install is
// apply.Run's, and the plan screen shows the command that would do the same
// without the interface.

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/apply"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
)

const (
	screenFound = iota
	screenNames
	screenComponents
	screenJoin
	screenReach
	screenPlan
	screenApply
)

var screenTitles = []string{"What it found", "Names", "Components", "Server", "Reach", "The plan", "Install"}

// Installer carries out an install. In the program it is apply.Run and
// apply.Verify; tests give it something that changes nothing.
type Installer interface {
	Run(ctx context.Context, p plan.Plan, f host.Facts, log apply.Log) (*apply.Result, error)
	Verify(ctx context.Context, res *apply.Result, o plan.Options, log apply.Log) []string
	// The same for a join: apply.JoinCheck, apply.Join and apply.JoinVerify.
	JoinCheck(ctx context.Context, o plan.Options, key string) error
	Join(ctx context.Context, p plan.Plan, f host.Facts, key string, log apply.Log) (*apply.Result, error)
	JoinVerify(ctx context.Context, res *apply.Result, console, webmail plan.Shape, log apply.Log) []string
}

// Wizard is the interface's state.
type Wizard struct {
	Facts     host.Facts
	Installer Installer

	o       plan.Options
	screen  int
	scroll  int
	message string // a refusal or a hint, shown above the footer

	fields []*field
	focus  int

	row    int
	shapes map[plan.Component]plan.Shape

	proxy int // 0 caddy, 1 snippets for your own, 2 none

	join joinState // the Join screen, when the server is skipped (join.go)

	built      plan.Plan
	buildErr   error
	confirming bool

	progress *progress
	cancel   context.CancelFunc
	stopping bool
	changed  chan struct{}
}

type field struct {
	label, hint string
	value       []rune
	cursor      int
	toggle      bool // a yes/no field, in value "yes" or ""
	secret      bool // typed but never shown: the API key
	placeholder func(o plan.Options) string
}

func (f *field) text() string { return string(f.value) }

// NewWizard starts at the first screen with what the flags default to.
func NewWizard(f host.Facts, inst Installer) *Wizard {
	w := &Wizard{Facts: f, Installer: inst, changed: make(chan struct{}, 1)}
	w.o.Dir = "/var/lib/inbuxa"
	under := func(prefix string) func(plan.Options) string {
		return func(o plan.Options) string {
			if o.Domain == "" {
				return prefix + "DOMAIN"
			}
			return prefix + o.Domain
		}
	}
	w.fields = []*field{
		{label: "Mail domain", hint: "the domain people's addresses are at", placeholder: func(plan.Options) string { return "example.com" }},
		{label: "Mail server", hint: "the server's own name, which MX points at", placeholder: under("mail.")},
		{label: "Console", hint: "where the administration console is served", placeholder: under("admin.")},
		{label: "Webmail", hint: "where people read their mail", placeholder: under("webmail.")},
		{label: "ACME contact", hint: "where the certificate authority writes about certificates", placeholder: under("postmaster@")},
		{label: "Directory", hint: "where the installation lives", value: []rune(w.o.Dir)},
		{label: "Try it on loopback", hint: "no public ports, no certificates, no DNS: an evaluation on this machine", toggle: true},
	}
	for _, f := range w.fields {
		f.cursor = len(f.value)
	}
	w.join.fields = joinFields()
	w.shapes = map[plan.Component]plan.Shape{plan.Server: plan.Container, plan.Console: plan.Container, plan.Webmail: plan.Container}
	for _, c := range []plan.Component{plan.Server, plan.Console, plan.Webmail} {
		w.shapes[c] = w.defaultShape(c)
	}
	return w
}

// defaultShape is what the flags default to, a container, wherever the
// machine can run one or the installer can make it able to; the server goes
// on the host only where containers are out of reach altogether.
func (w *Wizard) defaultShape(c plan.Component) plan.Shape {
	if _, blocked := w.cellNote(c, plan.Container); !blocked {
		return plan.Container
	}
	if c == plan.Server && plan.Available(w.Facts, c, plan.Host).OK {
		return plan.Host
	}
	return plan.Container
}

// Changed is when an install moved on.
func (w *Wizard) Changed() <-chan struct{} { return w.changed }

func (w *Wizard) poke() {
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

// Options is what the screens filled in, as the flags would have.
func (w *Wizard) Options() plan.Options {
	o := w.o
	o.Domain = strings.TrimSpace(w.fields[0].text())
	o.MailHost = strings.TrimSpace(w.fields[1].text())
	o.ConsoleHost = strings.TrimSpace(w.fields[2].text())
	o.WebmailHost = strings.TrimSpace(w.fields[3].text())
	o.ACMEEmail = strings.TrimSpace(w.fields[4].text())
	o.Dir = strings.TrimSpace(w.fields[5].text())
	o.Local = w.fields[6].text() == "yes"
	o.Proxy = []string{"caddy", "snippets", "none"}[w.proxy]
	o.Choices = nil
	for _, c := range []plan.Component{plan.Server, plan.Console, plan.Webmail} {
		o.Choices = append(o.Choices, plan.Choice{Component: c, Shape: w.shapes[c]})
	}
	if w.joining() {
		w.joinOptions(&o)
	}
	// What the machine lacks for the shapes chosen is installed rather
	// than refused: the components screen says what that will be.
	o.InstallDeps = len(w.needs(o)) > 0
	return o
}

func (w *Wizard) needs(o plan.Options) []string {
	p, _ := plan.Build(w.Facts, withDeps(o))
	var names []string
	for _, n := range p.Needs {
		names = append(names, n.Name)
	}
	return names
}

func withDeps(o plan.Options) plan.Options {
	o.InstallDeps = true
	if o.Domain == "" && !o.Local {
		o.Domain = "example.com" // enough to build a plan to read the needs from
	}
	return o
}

// Update handles one key.
func (w *Wizard) Update(k Key) bool {
	w.settle()
	if w.screen == screenApply {
		return w.updateApply(k)
	}
	if w.join.checking() {
		return w.updateChecking(k)
	}
	if k.Kind == KeyCtrlC {
		return true
	}
	if w.confirming {
		switch {
		case k.Kind == KeyRune && (k.Rune == 'y' || k.Rune == 'Y'):
			w.confirming = false
			w.startInstall()
		default:
			w.confirming = false
			w.message = "Nothing has happened."
		}
		return false
	}
	w.message = ""
	// q quits everywhere but in a text field, where it is a letter
	if k.Kind == KeyRune && k.Rune == 'q' && !w.typing() {
		return true
	}
	if k.Kind == KeyEsc && w.screen > screenFound {
		w.screen--
		if w.screen == screenJoin && !w.joining() {
			w.screen--
		}
		w.scroll = 0
		return false
	}
	switch w.screen {
	case screenFound:
		w.updateScroll(k)
		if k.Kind == KeyEnter {
			w.next()
		}
	case screenNames:
		w.updateNames(k)
	case screenComponents:
		w.updateComponents(k)
	case screenJoin:
		w.updateJoin(k)
	case screenReach:
		w.updateReach(k)
	case screenPlan:
		w.updateScroll(k)
		if k.Kind == KeyEnter {
			switch {
			case w.buildErr != nil:
				w.message = "The plan can't be carried out as it stands: Esc to go back and change it."
			case !w.Facts.Root:
				w.message = "Installing needs root: run it again as root (sudo inbuxa). Nothing has happened."
			case w.joining() && !w.joinChecked():
				w.message = "The server hasn't been asked about this exact join: Esc back to the Server screen and ask it again."
			default:
				w.confirming = true
			}
		}
	}
	return false
}

func (w *Wizard) next() {
	w.screen++
	if w.screen == screenJoin && !w.joining() {
		w.screen++
	}
	w.scroll = 0
	if w.screen == screenPlan {
		w.built, w.buildErr = plan.Build(w.Facts, w.Options())
	}
}

func (w *Wizard) updateScroll(k Key) {
	switch k.Kind {
	case KeyUp:
		w.scroll--
	case KeyDown:
		w.scroll++
	case KeyPgUp:
		w.scroll -= 10
	case KeyPgDn:
		w.scroll += 10
	case KeyHome:
		w.scroll = 0
	}
	if w.scroll < 0 {
		w.scroll = 0
	}
}

func (w *Wizard) updateNames(k Key) {
	f := w.fields[w.focus]
	switch k.Kind {
	case KeyUp, KeyBackTab:
		if w.focus > 0 {
			w.focus--
		}
	case KeyDown, KeyTab:
		if w.focus < len(w.fields)-1 {
			w.focus++
		}
	case KeyEnter:
		if w.focus < len(w.fields)-1 {
			w.focus++
			return
		}
		if why := w.namesProblem(); why != "" {
			w.message = why
			return
		}
		w.next()
	case KeyRune:
		if f.toggle {
			if k.Rune == ' ' || k.Rune == 'y' || k.Rune == 'n' {
				w.toggle(f, k.Rune)
			}
			return
		}
		f.edit(k)
	default:
		f.edit(k)
	}
}

// edit is a key in a text field: typing, deleting and moving the cursor.
func (f *field) edit(k Key) {
	switch k.Kind {
	case KeyRune:
		if f.toggle {
			return
		}
		f.value = append(f.value[:f.cursor], append([]rune{k.Rune}, f.value[f.cursor:]...)...)
		f.cursor++
	case KeyBackspace:
		if !f.toggle && f.cursor > 0 {
			f.value = append(f.value[:f.cursor-1], f.value[f.cursor:]...)
			f.cursor--
		}
	case KeyDelete:
		if !f.toggle && f.cursor < len(f.value) {
			f.value = append(f.value[:f.cursor], f.value[f.cursor+1:]...)
		}
	case KeyLeft:
		if f.cursor > 0 {
			f.cursor--
		}
	case KeyRight:
		if f.cursor < len(f.value) {
			f.cursor++
		}
	case KeyHome:
		f.cursor = 0
	case KeyEnd:
		f.cursor = len(f.value)
	case KeyCtrlU:
		if !f.toggle {
			f.value, f.cursor = nil, 0
		}
	}
}

func (w *Wizard) toggle(f *field, r rune) {
	on := f.text() == "yes"
	switch r {
	case 'y':
		on = true
	case 'n':
		on = false
	default:
		on = !on
	}
	if on {
		f.value = []rune("yes")
		// A loopback evaluation still needs a domain for its mailboxes;
		// the one reserved for testing is the honest default.
		if strings.TrimSpace(w.fields[0].text()) == "" {
			w.fields[0].value = []rune("example.test")
			w.fields[0].cursor = len(w.fields[0].value)
		}
	} else {
		f.value = nil
	}
}

// namesProblem is why the names can't be used yet, or "".
func (w *Wizard) namesProblem() string {
	o := w.Options()
	if o.Domain == "" {
		return "A mail domain is needed (or turn on \"Try it on loopback\")."
	}
	for i, v := range []string{o.Domain, o.MailHost, o.ConsoleHost, o.WebmailHost} {
		if v != "" && (strings.ContainsAny(v, " /:@") || !strings.Contains(v, ".")) {
			return fmt.Sprintf("%s %q is not a name like example.com.", w.fields[i].label, v)
		}
	}
	if o.ACMEEmail != "" && !strings.Contains(o.ACMEEmail, "@") {
		return "The ACME contact is an email address."
	}
	if o.Dir == "" || !filepath.IsAbs(o.Dir) {
		return "The directory is a full path, like /var/lib/inbuxa."
	}
	return ""
}

var shapeOrder = []plan.Shape{plan.Skip, plan.Container, plan.Host}

func (w *Wizard) updateComponents(k Key) {
	comps := []plan.Component{plan.Server, plan.Console, plan.Webmail}
	c := comps[w.row]
	idx := 0
	for i, s := range shapeOrder {
		if s == w.shapes[c] {
			idx = i
		}
	}
	switch k.Kind {
	case KeyUp, KeyBackTab:
		if w.row > 0 {
			w.row--
		}
	case KeyDown, KeyTab:
		if w.row < len(comps)-1 {
			w.row++
		}
	case KeyLeft:
		if idx > 0 {
			w.shapes[c] = shapeOrder[idx-1]
		}
	case KeyRight:
		if idx < len(shapeOrder)-1 {
			w.shapes[c] = shapeOrder[idx+1]
		}
	case KeyRune:
		switch k.Rune {
		case 's':
			w.shapes[c] = plan.Skip
		case 'c':
			w.shapes[c] = plan.Container
		case 'h':
			w.shapes[c] = plan.Host
		}
	case KeyEnter:
		if why := w.componentsProblem(); why != "" {
			w.message = why
			return
		}
		w.next()
	}
}

// cellNote is what a choice means on this machine: "" when it simply works.
func (w *Wizard) cellNote(c plan.Component, s plan.Shape) (note string, blocked bool) {
	if s == plan.Skip {
		return "", false
	}
	a := plan.Available(w.Facts, c, s)
	if a.OK {
		return "", false
	}
	// Missing but fixable: the plan installs it, and says so.
	o := w.Options()
	o.Choices = []plan.Choice{{Component: c, Shape: s}}
	if _, err := plan.Build(w.Facts, withDeps(o)); err == nil {
		return a.Why + " -- the installer will install what is missing", false
	}
	return a.Why, true
}

func (w *Wizard) componentsProblem() string {
	any := false
	for _, c := range []plan.Component{plan.Server, plan.Console, plan.Webmail} {
		if w.shapes[c] != plan.Skip {
			any = true
		}
		if note, blocked := w.cellNote(c, w.shapes[c]); blocked {
			return fmt.Sprintf("%s can't be a %s install here: %s", c, w.shapes[c], note)
		}
	}
	if !any {
		return "Choose at least one component."
	}
	if w.joining() {
		if s := recordedServer(); s != "" {
			return fmt.Sprintf("This machine runs the mail server itself (a %s install), so its console and webmail are added with it, not joined to it: keep the mail server here.", s)
		}
	}
	// What each choice can do on its own is above; what the choices can't
	// do together -- a console of files on loopback, with no web server to
	// serve them -- only the whole plan says.
	if _, err := plan.Build(w.Facts, withDeps(w.Options())); err != nil {
		return err.Error()
	}
	return ""
}

func (w *Wizard) updateReach(k Key) {
	if w.Options().Local {
		if k.Kind == KeyEnter {
			w.next()
		}
		return
	}
	switch k.Kind {
	case KeyUp:
		if w.proxy > 0 {
			w.proxy--
		}
	case KeyDown:
		if w.proxy < 2 {
			w.proxy++
		}
	case KeyEnter:
		w.next()
	}
}

// View draws the current screen.
func (w *Wizard) View(width, height int) []string {
	if width < 60 || height < 16 {
		return []string{"", "  The window is too small for the installer.", "  Make it at least 60 by 16, or press q to quit."}
	}
	w.settle()
	var out []string
	var steps []string
	for i, t := range screenTitles {
		if i == screenJoin && !w.joining() {
			continue
		}
		if i == screenApply && w.joining() {
			t = "Join"
		}
		switch {
		case i == w.screen:
			steps = append(steps, Bold(Accent(t)))
		case i < w.screen:
			steps = append(steps, t)
		default:
			steps = append(steps, Dim(t))
		}
	}
	out = append(out, " "+Bold("inbuxa installer")+Dim("  ·  ")+strings.Join(steps, Dim(" › ")), "")

	var body []string
	var help string
	switch w.screen {
	case screenFound:
		body = indent(strings.Split(strings.TrimRight(plan.SurveyText(w.Facts), "\n"), "\n"))
		help = "Enter: continue   ↑↓: scroll   q: quit"
	case screenNames:
		body, help = w.viewNames(width)
	case screenComponents:
		body, help = w.viewComponents(width)
	case screenJoin:
		body, help = w.viewJoin(width)
	case screenReach:
		body, help = w.viewReach(width)
	case screenPlan:
		body, help = w.viewPlan(width)
	case screenApply:
		body, help = w.viewApply(width, height-5)
	}

	room := height - len(out) - 3
	if w.screen == screenFound || w.screen == screenPlan {
		if max := len(body) - room; w.scroll > max {
			w.scroll = max
		}
		if w.scroll < 0 {
			w.scroll = 0
		}
		body = body[w.scroll:]
	}
	if len(body) > room {
		body = append(body[:room-1], Dim("  … ↓ for more"))
	}
	out = append(out, body...)
	for len(out) < height-2 {
		out = append(out, "")
	}
	switch {
	case w.confirming && w.joining():
		out = append(out, " "+Bold(Warn("Join now? This changes this machine and the server's front ends. y to go ahead, any other key not to.")))
	case w.confirming:
		out = append(out, " "+Bold(Warn("Install now? This changes this machine. y to go ahead, any other key not to.")))
	case w.message != "":
		out = append(out, " "+Warn(w.message))
	default:
		out = append(out, "")
	}
	out = append(out, " "+Dim(help))
	return out
}

func indent(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = "  " + l
	}
	return out
}

func (w *Wizard) viewNames(width int) ([]string, string) {
	o := w.Options()
	local := o.Local
	body := []string{"  " + Bold("Where it will be reached"), ""}
	for i, f := range w.fields {
		label := Pad(f.label, 20)
		var val string
		switch {
		case f.toggle:
			val = "[ ]"
			if f.text() == "yes" {
				val = "[x]"
			}
			if i == w.focus {
				val = Reverse(val)
			}
		case len(f.value) == 0 && i != w.focus:
			val = Dim(f.placeholder(o))
		case i == w.focus:
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
		dimmed := local && i >= 1 && i <= 4
		line := "  "
		if i == w.focus {
			line = Accent("› ")
		}
		if dimmed {
			line += Dim(label + f.text())
		} else {
			line += label + val
		}
		body = append(body, line)
		if i == w.focus {
			body = append(body, "    "+Dim(Pad("", 18)+f.hint))
		}
	}
	if local {
		body = append(body, "", "  "+Dim("On loopback the names only name the mail domain; nothing is published."))
	}
	return body, "↑↓ Tab: move   type to edit   Space: toggle   Enter: next   Esc: back   Ctrl-C: quit"
}

func (w *Wizard) viewComponents(width int) ([]string, string) {
	body := []string{"  " + Bold("What goes on this machine, and how"), ""}
	comps := []plan.Component{plan.Server, plan.Console, plan.Webmail}
	labels := map[plan.Component]string{plan.Server: "Mail server", plan.Console: "Console", plan.Webmail: "Webmail"}
	for i, c := range comps {
		line := "  "
		if i == w.row {
			line = Accent("› ")
		}
		line += Pad(labels[c], 14)
		for _, s := range shapeOrder {
			cell := " " + string(s) + " "
			_, blocked := w.cellNote(c, s)
			switch {
			case w.shapes[c] == s && blocked:
				cell = Reverse(Bad(cell))
			case w.shapes[c] == s:
				cell = Reverse(cell)
			case blocked:
				cell = Dim(cell)
			}
			line += cell + "  "
		}
		body = append(body, line)
		if note, blocked := w.cellNote(c, w.shapes[c]); note != "" {
			style := Dim
			if blocked {
				style = Bad
			}
			for _, l := range WrapProse(note, width-20) {
				body = append(body, "                "+style(l))
			}
		}
	}
	body = append(body, "",
		"  "+Dim("container: an image, run with this machine's docker or podman"),
		"  "+Dim("host:      from its release, under systemd or as files a web server serves"),
		"  "+Dim("skip:      not on this machine; skip the mail server to join one that runs elsewhere"))
	return body, "↑↓: component   ←→ or s/c/h: choose   Enter: next   Esc: back   q: quit"
}

func (w *Wizard) viewReach(width int) ([]string, string) {
	body := []string{"  " + Bold("What answers on ports 80 and 443"), ""}
	if w.Options().Local {
		body = append(body, "  A loopback evaluation publishes nothing: no proxy, no certificates.",
			"  The server, the console and the webmail answer on 127.0.0.1 only.")
		return body, "Enter: continue   Esc: back   q: quit"
	}
	options := []struct{ name, about string }{
		{"Caddy, managed by the installer", "It holds 80 and 443, gets certificates for every name, and passes the server's own certificate challenges through."},
		{"Your own web server, with its configuration written", "Nothing here takes 80 or 443. The installer writes an nginx configuration and a Caddyfile fragment for the web server you run, and trusts it in front of the suite; loading it is yours."},
		{"Nothing in front", "Nothing here takes 80 or 443, and nothing is written. The server answers on 127.0.0.1:8081, the console on 127.0.0.1:8082, the webmail on 127.0.0.1:8080."},
	}
	for i, opt := range options {
		mark := "( )"
		if w.proxy == i {
			mark = Accent("(•)")
		}
		line := "  " + mark + " " + opt.name
		if w.proxy == i {
			line = "  " + mark + " " + Bold(opt.name)
		}
		body = append(body, line)
		for _, l := range WrapProse(opt.about, width-10) {
			body = append(body, "      "+Dim(l))
		}
		body = append(body, "")
	}
	return body, "↑↓: choose   Enter: next   Esc: back   q: quit"
}

func (w *Wizard) viewPlan(width int) ([]string, string) {
	if w.buildErr != nil {
		body := []string{"  " + Bad(Bold("This can't be carried out as it stands")), ""}
		for _, l := range WrapProse(w.buildErr.Error(), width-6) {
			body = append(body, "  "+l)
		}
		return body, "Esc: back   q: quit"
	}
	body := []string{"  " + Dim("Nothing has happened yet. This is everything that will."), ""}
	for _, l := range strings.Split(strings.TrimRight(w.built.String(), "\n"), "\n") {
		for _, wl := range Wrap(l, width-4) {
			body = append(body, "  "+wl)
		}
	}
	body = append(body, "", "  "+Dim("The same, without the interface:"))
	for _, l := range Wrap(Command(w.built.Options), width-6) {
		body = append(body, "    "+Accent(l))
	}
	verb := "install"
	if w.joining() {
		verb = "join"
	}
	return body, "Enter: " + verb + "   ↑↓ PgUp PgDn: scroll   Esc: back   q: quit"
}

// Command is the flags that do what the screens chose, leaving out what
// would be the default anyway.
func Command(o plan.Options) string {
	if o.ServerURL != "" {
		return joinCommand(o)
	}
	args := []string{"inbuxa", "install"}
	add := func(flag, v, def string) {
		if v != "" && v != def {
			args = append(args, flag, v)
		}
	}
	if o.Local {
		args = append(args, "--local")
	}
	add("--domain", o.Domain, "")
	add("--mail-host", o.MailHost, "mail."+o.Domain)
	add("--console-host", o.ConsoleHost, "admin."+o.Domain)
	add("--webmail-host", o.WebmailHost, "webmail."+o.Domain)
	add("--email", o.ACMEEmail, "postmaster@"+o.Domain)
	add("--server", string(o.Shape(plan.Server)), "container")
	add("--console", string(o.Shape(plan.Console)), "container")
	add("--webmail", string(o.Shape(plan.Webmail)), "container")
	if !o.Local {
		add("--proxy", o.Proxy, "caddy")
	}
	add("--dir", o.Dir, "/var/lib/inbuxa")
	if o.InstallDeps {
		args = append(args, "--install-deps")
	}
	return strings.Join(append(args, "--yes"), " ")
}

// ---- the install itself ----

// progress is what the install has said so far. apply calls it from its own
// goroutine; the screen reads it under the lock.
type progress struct {
	mu       sync.Mutex
	steps    []string
	lines    []string
	done     bool
	err      error
	result   *apply.Result
	problems []string
	extra    []string // after the summary: what a join leaves to say
	poke     func()
}

func (p *progress) Step(format string, a ...any) {
	p.mu.Lock()
	s := fmt.Sprintf(format, a...)
	p.steps = append(p.steps, s)
	p.lines = append(p.lines, s)
	p.mu.Unlock()
	p.poke()
}

func (p *progress) Info(format string, a ...any) {
	p.mu.Lock()
	p.lines = append(p.lines, "    "+fmt.Sprintf(format, a...))
	p.mu.Unlock()
	p.poke()
}

func (p *progress) Out() io.Writer { return lineWriter{p} }

type lineWriter struct{ p *progress }

func (l lineWriter) Write(b []byte) (int, error) {
	l.p.mu.Lock()
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			l.p.lines = append(l.p.lines, "    "+strings.TrimRight(line, "\r"))
		}
	}
	l.p.mu.Unlock()
	l.p.poke()
	return len(b), nil
}

func (w *Wizard) startInstall() {
	if w.joining() {
		w.startJoin()
		return
	}
	w.screen = screenApply
	w.message = ""
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	pr := &progress{poke: w.poke}
	w.progress = pr
	p := w.built
	go func() {
		res, err := w.Installer.Run(ctx, p, w.Facts, pr)
		var problems []string
		if err == nil {
			pr.Step("checking it works")
			problems = w.Installer.Verify(ctx, res, p.Options, pr)
		}
		pr.mu.Lock()
		pr.done, pr.err, pr.result, pr.problems = true, err, res, problems
		pr.mu.Unlock()
		pr.poke()
	}()
}

func (w *Wizard) updateApply(k Key) bool {
	pr := w.progress
	pr.mu.Lock()
	done := pr.done
	pr.mu.Unlock()
	if done {
		return k.Kind == KeyCtrlC || k.Kind == KeyEnter || (k.Kind == KeyRune && k.Rune == 'q')
	}
	if w.stopping {
		if k.Kind == KeyRune && (k.Rune == 'y' || k.Rune == 'Y') {
			w.cancel()
			w.message = "Stopping after the step that is running…"
		} else {
			w.message = "Carrying on."
		}
		w.stopping = false
		return false
	}
	if k.Kind == KeyCtrlC || (k.Kind == KeyRune && k.Rune == 'q') {
		w.stopping = true
		w.message = "Stop the install? It will be left part done; running it again picks up from there. y to stop."
	}
	return false
}

func (w *Wizard) viewApply(width, room int) ([]string, string) {
	pr := w.progress
	pr.mu.Lock()
	defer pr.mu.Unlock()
	var body []string
	for i, s := range pr.steps {
		mark := Good("✓")
		switch {
		case i == len(pr.steps)-1 && !pr.done:
			mark = Accent("›")
		case i == len(pr.steps)-1 && pr.err != nil:
			mark = Bad("✗")
		}
		body = append(body, "  "+mark+" "+s)
	}
	if pr.done {
		body = append(body, "")
		body = append(body, Summary(pr.result, pr.err, pr.problems)...)
		body = append(body, pr.extra...)
		return body, "Enter or q: leave (the summary stays in your terminal)"
	}
	// Under the steps, the last of what is being said
	logRoom := room - len(body) - 2
	if logRoom > 2 {
		body = append(body, "", "  "+Dim("─ "+strings.Repeat("─", width-8)))
		tail := pr.lines
		if len(tail) > logRoom-1 {
			tail = tail[len(tail)-(logRoom-1):]
		}
		for _, l := range tail {
			body = append(body, "  "+Dim(l))
		}
	}
	return body, "q or Ctrl-C: stop"
}

// Summary is what the install leaves the operator holding, as the flag path
// prints it; the interface shows it, then prints it again once the terminal
// is back, so it stays in the scrollback.
func Summary(res *apply.Result, err error, problems []string) []string {
	var out []string
	if err != nil {
		out = append(out, "  "+Bad(Bold("Stopped: "))+err.Error())
		if res != nil && res.AdminUser != "" {
			out = append(out, "", "  The administrator was already created; its password is in "+filepath.Join(res.Dir, "credentials.txt")+".")
		}
		return out
	}
	head := Good(Bold("Done."))
	if len(problems) > 0 {
		head = Warn(Bold("Installed, with problems to look at:"))
	}
	out = append(out, "  "+head+" "+res.Dir)
	for _, p := range problems {
		out = append(out, "    "+Warn("problem: ")+p)
	}
	row := func(k, v string) {
		if v != "" {
			out = append(out, fmt.Sprintf("    %-15s%s", k, v))
		}
	}
	row("administrator", res.AdminUser)
	row("first mailbox", res.FirstUser)
	if res.AdminUser != "" { // a join makes no accounts, so writes no passwords
		row("passwords", filepath.Join(res.Dir, "credentials.txt"))
	}
	row("console", res.ConsoleURL)
	row("webmail", res.WebmailURL)
	if res.Certificate != "" {
		row("certificate", "issued by "+res.Certificate)
	}
	row("dns records", res.ZoneFile)
	if res.ProxyConfig != "" {
		row("proxy config", res.ProxyConfig+" (load it into your web server)")
	}
	return out
}

// Outcome is how the install ended, once the interface has closed: the
// summary to print, and whether it succeeded.
func (w *Wizard) Outcome() (summary []string, ran, ok bool) {
	if w.progress == nil {
		return nil, false, true
	}
	pr := w.progress
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if !pr.done {
		if w.joining() {
			return []string{"  The join was left before it finished; run inbuxa again to pick it up."}, true, false
		}
		return []string{"  The install was left before it finished; run inbuxa again to pick it up."}, true, false
	}
	return append(Summary(pr.result, pr.err, pr.problems), pr.extra...), true, pr.err == nil && len(pr.problems) == 0
}
