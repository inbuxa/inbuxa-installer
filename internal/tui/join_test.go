// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/apply"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
)

func (f *fakeInstaller) JoinCheck(ctx context.Context, o plan.Options, key string) error {
	f.mu.Lock()
	f.checkedOn = append(f.checkedOn, key)
	hold := f.checkHold
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.checkErr
}

func (f *fakeInstaller) Join(ctx context.Context, p plan.Plan, _ host.Facts, key string, log apply.Log) (*apply.Result, error) {
	f.mu.Lock()
	f.joined, f.joinKey = &p, key
	f.mu.Unlock()
	log.Step("registering these front ends with %s", p.Options.ServerURL)
	return &apply.Result{Dir: p.Options.Dir, ConsoleURL: "https://admin." + p.Options.Domain, WebmailURL: "https://webmail." + p.Options.Domain, ServerURL: p.Options.ServerURL}, nil
}

func (f *fakeInstaller) JoinVerify(context.Context, *apply.Result, plan.Shape, plan.Shape, apply.Log) []string {
	return nil
}

const testKey = "k3y-that-must-never-show"

// The tests never read this machine's own install record.
func init() { recordedServer = func() string { return "" } }

// toJoin goes from the first screen to the Server screen: a domain, the
// defaults for the names, and the mail server skipped.
func toJoin(t *testing.T, w *Wizard, domain string) {
	t.Helper()
	press(w, KeyEnter)
	typeText(w, domain)
	for i := 0; i < len(w.fields); i++ {
		press(w, KeyEnter)
	}
	w.Update(Key{Kind: KeyRune, Rune: 's'}) // the mail server: skip
	press(w, KeyEnter)
	if w.screen != screenJoin {
		t.Fatalf("skipping the server did not lead to the Server screen, at %d: %q", w.screen, w.message)
	}
}

// waitCheck waits for the question to the server to be answered, then lets
// the wizard take the answer in, as the next redraw would.
func waitCheck(t *testing.T, w *Wizard) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for w.join.checking() {
		if time.Now().After(deadline) {
			t.Fatal("the check never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.View(100, 30)
}

func TestSkippingTheServerJoinsOne(t *testing.T) {
	inst := &fakeInstaller{}
	w := NewWizard(debian13(true), inst)
	toJoin(t, w, "example.org")

	head := w.View(100, 30)[0]
	if !strings.Contains(head, "Server") || !strings.Contains(head, "Join") || strings.Contains(head, "Install") {
		t.Errorf("the steps don't say this is a join: %q", head)
	}
	if got := w.Options().ServerURL; got != "https://mail.example.org" {
		t.Errorf("the server defaults to %q", got)
	}

	press(w, KeyDown, KeyDown) // to Replace, past an empty key
	press(w, KeyEnter)
	if !strings.Contains(w.message, "API key") || w.join.checking() {
		t.Fatalf("no key, and it asked the server anyway: %q", w.message)
	}

	press(w, KeyUp) // the key
	typeText(w, testKey)
	view := strings.Join(w.View(100, 30), "\n")
	if strings.Contains(view, testKey) || !strings.Contains(view, "••••") {
		t.Fatalf("the key is on screen:\n%s", view)
	}
	press(w, KeyDown, KeyEnter)
	waitCheck(t, w)
	if w.screen != screenReach || len(inst.checkedOn) != 1 || inst.checkedOn[0] != testKey {
		t.Fatalf("after the check: screen %d, %q, asked with %q", w.screen, w.message, inst.checkedOn)
	}

	press(w, KeyEnter) // reach
	if w.screen != screenPlan {
		t.Fatalf("not at the plan: %q", w.message)
	}
	view = strings.Join(w.View(140, 60), "\n")
	if !strings.Contains(view, "inbuxa join https://mail.example.org --key - ") || strings.Contains(view, testKey) {
		t.Errorf("the plan's command:\n%s", view)
	}

	press(w, KeyEnter)
	if !w.confirming || !strings.Contains(strings.Join(w.View(140, 30), "\n"), "Join now?") {
		t.Fatal("no confirmation before joining")
	}
	w.Update(Key{Kind: KeyRune, Rune: 'y'})
	waitDone(t, w)
	if inst.got != nil {
		t.Error("a join ran an install")
	}
	if inst.joined == nil || inst.joinKey != testKey || inst.joined.Options.ServerURL != "https://mail.example.org" || inst.joined.Options.Shape(plan.Server) != plan.Skip {
		t.Fatalf("joined %+v with %q", inst.joined, inst.joinKey)
	}
	if len(w.join.fields[joinKey].value) != 0 {
		t.Error("the key is still held after the join used it")
	}
	summary, ran, ok := w.Outcome()
	text := strings.Join(summary, "\n")
	if !ran || !ok || !strings.Contains(text, "joined to      https://mail.example.org") || !strings.Contains(text, "not kept here") || strings.Contains(text, "credentials.txt") {
		t.Errorf("outcome %v %v:\n%s", ran, ok, text)
	}
}

func TestAKeyTheServerRefusesStaysOnItsScreen(t *testing.T) {
	inst := &fakeInstaller{checkErr: errors.New("the server refused the API key; make one in the console under your account's API keys, then pass it with --key")}
	w := NewWizard(debian13(true), inst)
	toJoin(t, w, "example.org")
	press(w, KeyDown)
	typeText(w, testKey)
	press(w, KeyDown, KeyEnter)
	waitCheck(t, w)
	if w.screen != screenJoin || !strings.Contains(w.message, "refused the API key") {
		t.Fatalf("a refused key: screen %d, %q", w.screen, w.message)
	}
}

func TestWaitingOnTheServerCanBeStopped(t *testing.T) {
	inst := &fakeInstaller{checkHold: make(chan struct{})}
	w := NewWizard(debian13(true), inst)
	toJoin(t, w, "example.org")
	press(w, KeyDown)
	typeText(w, testKey)
	press(w, KeyDown, KeyEnter)
	if !w.join.checking() {
		t.Fatal("not asking the server")
	}
	if quit := w.Update(Key{Kind: KeyRune, Rune: 'q'}); quit || w.screen != screenJoin {
		t.Fatal("a key other than Esc did something while asking")
	}
	press(w, KeyEsc)
	if w.join.checking() || w.screen != screenJoin || !strings.Contains(w.message, "Stopped asking") {
		t.Fatalf("Esc did not stop waiting: %q", w.message)
	}
	close(inst.checkHold)
	time.Sleep(20 * time.Millisecond)
	w.View(100, 30)
	if w.screen != screenJoin {
		t.Error("an abandoned check moved the screens on when it finished")
	}
}

func TestAJoinChangedAfterItsCheckIsAskedAgain(t *testing.T) {
	inst := &fakeInstaller{}
	w := NewWizard(debian13(true), inst)
	toJoin(t, w, "example.org")
	press(w, KeyDown)
	typeText(w, testKey)
	press(w, KeyDown, KeyEnter)
	waitCheck(t, w)
	press(w, KeyEnter)                               // reach
	w.join.fields[joinReplace].value = []rune("yes") // what the server was not asked about
	press(w, KeyEnter)
	if w.confirming || !strings.Contains(w.message, "asked about this exact join") {
		t.Fatalf("a join the server didn't see was offered: %q", w.message)
	}
}

func TestAnInstallNeverShowsTheServerStep(t *testing.T) {
	w := NewWizard(debian13(true), &fakeInstaller{})
	if head := w.View(120, 30)[0]; strings.Contains(head, "Server") {
		t.Errorf("an install shows the join's step: %q", head)
	}
	toPlan(t, w, "example.org") // components → reach, never the Server screen
	press(w, KeyEsc, KeyEsc)
	if w.screen != screenComponents {
		t.Errorf("Esc from reach went to %d, not components", w.screen)
	}
}

func TestTheJoinCommandLeavesOutWhatJoinWouldWorkOut(t *testing.T) {
	o := plan.Options{ServerURL: "https://mail.example.org", Domain: "example.org", Dir: "/var/lib/inbuxa", Proxy: "caddy",
		Choices: []plan.Choice{{Component: plan.Server, Shape: plan.Skip}, {Component: plan.Console, Shape: plan.Container}, {Component: plan.Webmail, Shape: plan.Host}}}
	if got, want := Command(o), "inbuxa join https://mail.example.org --key - --webmail host --yes"; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
	// The names the plan fills in are the defaults, and left out too.
	o.ConsoleHost, o.WebmailHost, o.ACMEEmail = "admin.example.org", "webmail.example.org", "postmaster@example.org"
	if got, want := Command(o), "inbuxa join https://mail.example.org --key - --webmail host --yes"; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestAMachineThatRunsTheServerIsNotJoinedToIt(t *testing.T) {
	was := recordedServer
	recordedServer = func() string { return "host" }
	defer func() { recordedServer = was }()
	w := NewWizard(debian13(true), &fakeInstaller{})
	press(w, KeyEnter)
	typeText(w, "example.org")
	for i := 0; i < len(w.fields); i++ {
		press(w, KeyEnter)
	}
	w.Update(Key{Kind: KeyRune, Rune: 's'})
	press(w, KeyEnter)
	if w.screen != screenComponents || !strings.Contains(w.message, "runs the mail server itself") {
		t.Fatalf("a join on the server's own machine got through: screen %d, %q", w.screen, w.message)
	}
}
