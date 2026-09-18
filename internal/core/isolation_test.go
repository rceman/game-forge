package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rceman/game-forge/internal/browser"
	"github.com/rceman/game-forge/internal/config"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/internal/process"
)

// writeManifest plants a minimal project manifest in dir. Two roots may share
// the same project.id; they must never share a project key.
func writeManifest(t *testing.T, dir, id, url string) {
	t.Helper()
	y := "contract: game-forge/v1\n" +
		"project:\n  id: " + id + "\n  type: web\n" +
		"server:\n  dev:\n    url: \"" + url + "\"\n    command: [\"true\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "game-forge.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeProvider is a browser.Provider whose Open blocks until released. It lets
// a test observe exactly when a second operation's browser work begins.
type fakeProvider struct {
	ns      string
	entered chan<- string
	release chan struct{}
	closed  *int32
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Check(context.Context) (*browser.CheckResult, error) {
	return &browser.CheckResult{OK: true}, nil
}
func (f *fakeProvider) Open(ctx context.Context, _ string) error {
	select {
	case f.entered <- f.ns:
	default:
	}
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *fakeProvider) Navigate(context.Context, string) error      { return nil }
func (f *fakeProvider) Reload(context.Context) error                { return nil }
func (f *fakeProvider) SetViewport(context.Context, int, int) error { return nil }
func (f *fakeProvider) Eval(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}
func (f *fakeProvider) Click(context.Context, string) error          { return nil }
func (f *fakeProvider) Press(context.Context, string) error          { return nil }
func (f *fakeProvider) Screenshot(context.Context, string) error     { return nil }
func (f *fakeProvider) PageErrors(context.Context) ([]string, error) { return nil, nil }
func (f *fakeProvider) ConsoleErrors(context.Context) ([]string, error) {
	return nil, nil
}
func (f *fakeProvider) ClearErrors(context.Context) error { return nil }
func (f *fakeProvider) Renderer(context.Context) (*browser.RendererInfo, error) {
	return &browser.RendererInfo{}, nil
}
func (f *fakeProvider) Close(context.Context) error {
	atomic.AddInt32(f.closed, 1)
	return nil
}

// providerFactory returns a Core.newBrowser that records each created fake so
// the test can count creations and release Opens individually.
type providerFactory struct {
	entered chan string
	mu      sync.Mutex
	fakes   []*fakeProvider
}

func (pf *providerFactory) install(c *Core) {
	c.newBrowser = func(_ *config.Config, ns string) browser.Provider {
		f := &fakeProvider{ns: ns, entered: pf.entered, release: make(chan struct{}), closed: new(int32)}
		pf.mu.Lock()
		pf.fakes = append(pf.fakes, f)
		pf.mu.Unlock()
		return f
	}
}

func (pf *providerFactory) count() int {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	return len(pf.fakes)
}

func (pf *providerFactory) releaseAll() {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	for _, f := range pf.fakes {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	}
}

type openResult struct {
	sess    *browser.Session
	cleanup func()
	err     error
}

// TestBrowserLockSerializesSameNamespace proves two operations on one shared
// namespace cannot overlap: the second does not even create its provider until
// the first releases the lock through its cleanup.
func TestBrowserLockSerializesSameNamespace(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	writeManifest(t, dir, "dup", "http://127.0.0.1:59998/")
	rt := NewRuntime(true) // KeepAlive → shared project namespace
	pf := &providerFactory{entered: make(chan string, 8)}

	c1, err := newCore(rt, dir, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := newCore(rt, dir, "run-b")
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	pf.install(c1)
	pf.install(c2)

	r1 := make(chan openResult, 1)
	r2 := make(chan openResult, 1)
	go func() {
		s, cl, e := c1.openBrowserRaw(context.Background(), "http://x/")
		r1 <- openResult{s, cl, e}
	}()
	go func() {
		s, cl, e := c2.openBrowserRaw(context.Background(), "http://x/")
		r2 <- openResult{s, cl, e}
	}()

	// Whichever operation wins the lock enters Open first; the loser must be
	// blocked in lockBrowser, which runs BEFORE the provider is even created.
	select {
	case <-pf.entered:
	case res := <-r1:
		t.Fatalf("op1 failed before open: %v", res.err)
	case res := <-r2:
		t.Fatalf("op2 failed before open: %v", res.err)
	case <-time.After(5 * time.Second):
		t.Fatal("no Open entered within 5s")
	}
	if n := pf.count(); n != 1 {
		t.Fatalf("expected exactly 1 provider while the namespace is locked, got %d", n)
	}
	// Let the winner's Open finish. openBrowserRaw returns but keeps the lock.
	pf.fakes[0].release <- struct{}{}
	var winner openResult
	var loser chan openResult
	select {
	case winner = <-r1:
		loser = r2
	case winner = <-r2:
		loser = r1
	}
	if winner.err != nil {
		t.Fatal(winner.err)
	}
	// The lock survives the return: the loser still has no provider.
	if n := pf.count(); n != 1 {
		t.Fatalf("namespace lock released too early: %d providers", n)
	}

	// Releasing the operation lock lets the loser proceed.
	winner.cleanup()
	<-pf.entered
	if n := pf.count(); n != 2 {
		t.Fatalf("expected 2 providers after unlock, got %d", n)
	}
	pf.releaseAll()
	res := <-loser
	if res.err != nil {
		t.Fatal(res.err)
	}
	res.cleanup()
}

// TestBrowserLockDifferentNamespaces proves independent namespaces do not
// serialize against each other — there is no global browser lock.
func TestBrowserLockDifferentNamespaces(t *testing.T) {
	isolate(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeManifest(t, dirA, "dup", "http://127.0.0.1:59998/")
	writeManifest(t, dirB, "dup", "http://127.0.0.1:59998/")
	rt := NewRuntime(true)
	pf := &providerFactory{entered: make(chan string, 8)}

	cA, _ := newCore(rt, dirA, "run-a")
	defer cA.Close()
	cB, _ := newCore(rt, dirB, "run-b")
	defer cB.Close()
	pf.install(cA)
	pf.install(cB)

	ra := make(chan openResult, 1)
	rb := make(chan openResult, 1)
	go func() {
		s, cl, e := cA.openBrowserRaw(context.Background(), "http://x/")
		ra <- openResult{s, cl, e}
	}()
	go func() {
		s, cl, e := cB.openBrowserRaw(context.Background(), "http://x/")
		rb <- openResult{s, cl, e}
	}()

	// Both Opens must start without waiting for each other: two entered
	// signals while the first is still blocked.
	<-pf.entered
	<-pf.entered
	if n := pf.count(); n != 2 {
		t.Fatalf("independent namespaces serialized: %d providers", n)
	}
	pf.releaseAll()
	// Let both finish and release their locks before the test's TempDir is
	// cleaned up, so no goroutine is still writing resources.
	for _, r := range []openResult{<-ra, <-rb} {
		if r.err != nil {
			t.Fatal(r.err)
		}
		r.cleanup()
	}
}

// TestMultiProjectIsolation proves two checkouts with the same project.id and
// the same "dev" server name never share resources: status, lease refresh and
// stop for A never touch B, and browser namespaces differ.
func TestMultiProjectIsolation(t *testing.T) {
	isolate(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	// Deliberately identical project.id, server name AND url: worst case.
	writeManifest(t, dirA, "dup", "http://127.0.0.1:59998/")
	writeManifest(t, dirB, "dup", "http://127.0.0.1:59998/")

	rt := NewRuntime(true)
	cA, err := newCore(rt, dirA, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cA.Close()
	cB, err := newCore(rt, dirB, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cB.Close()

	keyA, err := cA.projectKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := cB.projectKey()
	if err != nil {
		t.Fatal(err)
	}
	if keyA == keyB {
		t.Fatalf("two roots produced the same project key %q", keyA)
	}
	// Browser namespaces are project-keyed and therefore distinct.
	nsA, _ := cA.sharedNamespace()
	nsB, _ := cB.sharedNamespace()
	if nsA == nsB {
		t.Fatalf("two projects share a browser namespace %q", nsA)
	}

	// Register a "dev" server record for each project.
	recA := &process.Resource{
		ID: "server-" + keyA + "-dev-111", RunID: "ra", Project: "dup", ProjectKey: keyA,
		Kind: process.KindServer, Provider: "local", PID: deadPID,
		Metadata: map[string]string{"name": "dev", "url": "http://127.0.0.1:59998/", "pgid": "0"},
	}
	recB := &process.Resource{
		ID: "server-" + keyB + "-dev-222", RunID: "rb", Project: "dup", ProjectKey: keyB,
		Kind: process.KindServer, Provider: "local", PID: deadPID,
		Metadata: map[string]string{"name": "dev", "url": "http://127.0.0.1:59998/", "pgid": "0"},
	}
	if recA.ID == recB.ID {
		t.Fatal("resource ids collided across projects")
	}
	if err := cA.reg.Register(recA); err != nil {
		t.Fatal(err)
	}
	if err := cB.reg.Register(recB); err != nil {
		t.Fatal(err)
	}

	// Status for A reports only A's server as owned.
	ctxA := op.WithCwd(context.Background(), dirA)
	out, err := rt.serverReport(ctxA, json.RawMessage(`{"server":"dev"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	st := out.(serverStateOut)
	if st.State != "owned" || st.ID != recA.ID {
		t.Fatalf("A's status should show its own server, got %+v", st)
	}

	// Lease refresh on A must not touch B's record.
	bExpiresB := recB.Expires
	cA.refreshServerLease("dev", keyA, time.Hour)
	gotB, err := cB.reg.Get(recB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gotB.Expires.Equal(bExpiresB) {
		t.Error("refreshing A's lease changed B's record")
	}
	gotA, err := cA.reg.Get(recA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.Expires.Before(time.Now().Add(30 * time.Minute)) {
		t.Error("A's lease was not refreshed")
	}

	// Stopping A must not touch B.
	id, _, stopped, err := cA.stopNamedServer("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !stopped || id != recA.ID {
		t.Fatalf("expected to stop A's record %s, got %s", recA.ID, id)
	}
	if _, err := cB.reg.Get(recB.ID); err != nil {
		t.Error("stopping A removed B's record")
	}
	if _, err := cA.reg.Get(recA.ID); err == nil {
		t.Error("A's record should have been removed")
	}
}
