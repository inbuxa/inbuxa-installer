// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/apply"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
)

// debian13 is a machine where everything the installer offers can work.
func debian13(root bool) host.Facts {
	return host.Facts{
		OS:       host.OSInfo{ID: "debian", VersionID: "13", Pretty: "Debian GNU/Linux 13 (trixie)", Family: "debian"},
		Root:     root,
		Systemd:  true,
		Runtime:  host.Runtime{Kind: "docker", Present: true, Usable: true, Version: "26.1", Compose: "v2"},
		Glibc:    host.Glibc{Version: "2.41", Major: 2, Minor: 41},
		MemoryMB: 4096, DiskFreeGB: 50,
	}
}

// fakeInstaller changes nothing and remembers what it was asked to do.
type fakeInstaller struct {
	mu   sync.Mutex
	got  *plan.Plan
	fail error
	hold chan struct{} // when set, Run waits on it

	// joins (join_test.go)
	checkErr  error
	checkHold chan struct{} // when set, JoinCheck waits on it
	checkedOn []string      // the keys JoinCheck was given
	joined    *plan.Plan
	joinKey   string
}

func (f *fakeInstaller) Run(ctx context.Context, p plan.Plan, _ host.Facts, log apply.Log) (*apply.Result, error) {
	f.mu.Lock()
	f.got = &p
	f.mu.Unlock()
	log.Step("writing the deployment into %s", p.Options.Dir)
	log.Info("compose.yaml, .env")
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.fail != nil {
		return nil, f.fail
	}
	return &apply.Result{Dir: p.Options.Dir, AdminUser: "admin@" + p.Options.Domain, FirstUser: "postmaster@" + p.Options.Domain}, nil
}

func (f *fakeInstaller) Verify(context.Context, *apply.Result, plan.Options, apply.Log) []string {
	return nil
}

func typeText(w *Wizard, s string) {
	for _, r := range s {
		w.Update(Key{Kind: KeyRune, Rune: r})
	}
}

func press(w *Wizard, kinds ...KeyKind) {
	for _, k := range kinds {
		w.Update(Key{Kind: k})
	}
}

// toPlan goes from the first screen to the plan with a domain and the
// defaults everywhere else.
func toPlan(t *testing.T, w *Wizard, domain string) {
	t.Helper()
	press(w, KeyEnter) // what it found
	typeText(w, domain)
	for i := 0; i < len(w.fields); i++ {
		press(w, KeyEnter)
	}
	if w.screen != screenComponents {
		t.Fatalf("names did not lead on: %q", w.message)
	}
	press(w, KeyEnter) // components
	press(w, KeyEnter) // reach
	if w.screen != screenPlan {
		t.Fatalf("not at the plan, at %d: %q", w.screen, w.message)
	}
}

func TestTheDefaultsAreTheFlagsDefaults(t *testing.T) {
	w := NewWizard(debian13(true), &fakeInstaller{})
	toPlan(t, w, "example.org")
	if w.buildErr != nil {
		t.Fatal(w.buildErr)
	}
	if got := Command(w.built.Options); got != "inbuxa install --domain example.org --yes" {
		t.Errorf("command = %q", got)
	}
	o := w.Options()
	if o.Proxy != "caddy" || o.Dir != "/var/lib/inbuxa" || o.Local || o.InstallDeps {
		t.Errorf("options = %+v", o)
	}
}

func TestLoopbackFillsAnEvaluationDomain(t *testing.T) {
	w := NewWizard(debian13(true), &fakeInstaller{})
	press(w, KeyEnter)
	for w.focus < len(w.fields)-1 {
		press(w, KeyDown)
	}
	w.Update(Key{Kind: KeyRune, Rune: ' '})
	press(w, KeyEnter, KeyEnter, KeyEnter)
	if w.screen != screenPlan || w.buildErr != nil {
		t.Fatalf("screen %d, %v, %q", w.screen, w.buildErr, w.message)
	}
	if got := Command(w.built.Options); got != "inbuxa install --local --domain example.test --yes" {
		t.Errorf("command = %q", got)
	}
}

func TestTheServerOnTheHost(t *testing.T) {
	w := NewWizard(debian13(true), &fakeInstaller{})
	press(w, KeyEnter)
	typeText(w, "example.org")
	for i := 0; i < len(w.fields); i++ {
		press(w, KeyEnter)
	}
	w.Update(Key{Kind: KeyRune, Rune: 'h'}) // the server row is first
	press(w, KeyEnter, KeyEnter)
	if w.buildErr != nil {
		t.Fatal(w.buildErr)
	}
	if got := Command(w.built.Options); !strings.Contains(got, "--server host") {
		t.Errorf("command = %q", got)
	}
}

func TestWhatCannotBeBuiltIsRefusedOnItsScreen(t *testing.T) {
	w := NewWizard(debian13(true), &fakeInstaller{})
	press(w, KeyEnter)
	for w.focus < len(w.fields)-1 {
		press(w, KeyDown)
	}
	w.Update(Key{Kind: KeyRune, Rune: ' '}) // loopback: no web server
	press(w, KeyEnter)
	if w.screen != screenComponents {
		t.Fatalf("not at the components: %q", w.message)
	}
	press(w, KeyDown)                       // the console
	w.Update(Key{Kind: KeyRune, Rune: 'h'}) // on the host: files, and nothing to serve them
	press(w, KeyEnter)
	if w.screen != screenComponents || !strings.Contains(w.message, "web server") {
		t.Errorf("a console host install on loopback got through: screen %d, %q", w.screen, w.message)
	}
	w.Update(Key{Kind: KeyRune, Rune: 's'}) // no console
	press(w, KeyDown)
	w.Update(Key{Kind: KeyRune, Rune: 's'}) // no webmail
	press(w, KeyUp, KeyUp)
	w.Update(Key{Kind: KeyRune, Rune: 's'}) // and no server: nothing at all
	press(w, KeyEnter)
	if w.screen != screenComponents || !strings.Contains(w.message, "at least one") {
		t.Errorf("nothing at all got through: %q", w.message)
	}
	press(w, KeyDown)
	w.Update(Key{Kind: KeyRune, Rune: 'c'}) // a console, for a server elsewhere: a join
	press(w, KeyEnter)
	if w.screen != screenJoin {
		t.Errorf("front ends without a server did not lead to the Server screen: %d, %q", w.screen, w.message)
	}
}

func TestANameThatIsNotOneIsRefused(t *testing.T) {
	w := NewWizard(debian13(true), &fakeInstaller{})
	press(w, KeyEnter)
	typeText(w, "not a domain")
	for i := 0; i < len(w.fields); i++ {
		press(w, KeyEnter)
	}
	if w.screen != screenNames || !strings.Contains(w.message, "not a name") {
		t.Errorf("screen %d, %q", w.screen, w.message)
	}
	// Ctrl-U clears it, and a real one goes through
	w.focus = 0
	press(w, KeyCtrlU)
	typeText(w, "example.org")
	w.focus = len(w.fields) - 1
	press(w, KeyEnter)
	if w.screen != screenComponents {
		t.Errorf("a good name was refused: %q", w.message)
	}
}

func TestInstallingNeedsRootAndAYes(t *testing.T) {
	inst := &fakeInstaller{}
	w := NewWizard(debian13(false), inst)
	toPlan(t, w, "example.org")
	press(w, KeyEnter)
	if w.confirming || !strings.Contains(w.message, "root") {
		t.Fatalf("not root, and it offered to install: %q", w.message)
	}

	w = NewWizard(debian13(true), inst)
	toPlan(t, w, "example.org")
	press(w, KeyEnter)
	if !w.confirming {
		t.Fatal("no confirmation before installing")
	}
	w.Update(Key{Kind: KeyRune, Rune: 'n'})
	if w.screen != screenPlan || inst.got != nil {
		t.Fatal("a no installed anyway")
	}
	press(w, KeyEnter)
	w.Update(Key{Kind: KeyRune, Rune: 'y'})
	waitDone(t, w)
	if inst.got == nil || inst.got.Options.Domain != "example.org" {
		t.Fatalf("installed %+v", inst.got)
	}
	summary, ran, ok := w.Outcome()
	if !ran || !ok || !strings.Contains(strings.Join(summary, "\n"), "admin@example.org") {
		t.Errorf("outcome %v %v %q", ran, ok, summary)
	}
	view := strings.Join(w.View(100, 30), "\n")
	if !strings.Contains(view, "Done.") || !strings.Contains(view, "credentials.txt") {
		t.Errorf("the last screen:\n%s", view)
	}
}

func TestStoppingAnInstallAsksFirst(t *testing.T) {
	inst := &fakeInstaller{hold: make(chan struct{})}
	w := NewWizard(debian13(true), inst)
	toPlan(t, w, "example.org")
	press(w, KeyEnter)
	w.Update(Key{Kind: KeyRune, Rune: 'y'})
	if quit := w.Update(Key{Kind: KeyCtrlC}); quit || !w.stopping {
		t.Fatal("Ctrl-C during an install did not ask first")
	}
	w.Update(Key{Kind: KeyRune, Rune: 'y'})
	waitDone(t, w)
	_, ran, ok := w.Outcome()
	if !ran || ok {
		t.Errorf("a stopped install reported ok=%v", ok)
	}
}

func TestASmallWindowSaysSo(t *testing.T) {
	w := NewWizard(debian13(true), &fakeInstaller{})
	if v := strings.Join(w.View(40, 10), " "); !strings.Contains(v, "too small") {
		t.Errorf("view = %q", v)
	}
	for _, line := range w.View(80, 24) {
		if Width(Fit(line, 80)) > 80 {
			t.Errorf("a line is wider than the window: %q", line)
		}
	}
}

func waitDone(t *testing.T, w *Wizard) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.progress.mu.Lock()
		done := w.progress.done
		w.progress.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the install never finished")
}

func TestNoRuntimeStillDefaultsToContainersTheInstallerWillAdd(t *testing.T) {
	f := debian13(true)
	f.Runtime = host.Runtime{Why: "no container runtime: neither docker nor podman is installed"}
	w := NewWizard(f, &fakeInstaller{})
	for _, c := range []plan.Component{plan.Server, plan.Console, plan.Webmail} {
		if w.shapes[c] != plan.Container {
			t.Errorf("%s defaults to %s with docker installable, as the flags would not", c, w.shapes[c])
		}
	}
	toPlan(t, w, "example.org")
	if w.buildErr != nil {
		t.Fatal(w.buildErr)
	}
	if got := Command(w.built.Options); got != "inbuxa install --domain example.org --install-deps --yes" {
		t.Errorf("command = %q", got)
	}
}
