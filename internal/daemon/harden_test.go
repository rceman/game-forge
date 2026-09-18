package daemon

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/rceman/game-forge/internal/op"
)

// streamEvents runs a streamed request and returns its decoded NDJSON events.
func streamEvents(t *testing.T, srv string, body string) []op.Event {
	t.Helper()
	req, err := http.NewRequest("POST", srv+"/v1/run", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var evs []op.Event
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev op.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("bad event %q: %v", sc.Text(), err)
		}
		evs = append(evs, ev)
	}
	return evs
}

// TestStreamRunIDStableWithinStream proves every event of one stream carries
// the same non-empty run id.
func TestStreamRunIDStableWithinStream(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	evs := streamEvents(t, srv.URL, `{"v":1,"op":"test.stream","args":{}}`)
	if len(evs) < 3 {
		t.Fatalf("expected start+2 stages+done, got %d events", len(evs))
	}
	run := evs[0].Run
	if run == "" {
		t.Fatal("start event has no run id")
	}
	for _, ev := range evs {
		if ev.Run != run {
			t.Fatalf("event %v has run %q, want %q", ev.Ev, ev.Run, run)
		}
	}
}

// TestStreamRunIDUniqueAcrossRuns proves two streams never share a run id.
func TestStreamRunIDUniqueAcrossRuns(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	a := streamEvents(t, srv.URL, `{"v":1,"op":"test.stream","args":{}}`)
	b := streamEvents(t, srv.URL, `{"v":1,"op":"test.stream","args":{}}`)
	if a[0].Run == b[0].Run {
		t.Fatalf("two streams share run id %q", a[0].Run)
	}
	if !strings.HasPrefix(a[0].Run, "r_") {
		t.Errorf("run id %q lacks the r_ prefix", a[0].Run)
	}
}

// TestStreamRunIDConcurrent proves concurrent streams get distinct run ids.
func TestStreamRunIDConcurrent(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	const n = 8
	runs := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			evs := streamEvents(t, srv.URL, `{"v":1,"op":"test.stream","args":{}}`)
			runs[i] = evs[0].Run
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, r := range runs {
		if r == "" {
			t.Fatal("empty run id")
		}
		if seen[r] {
			t.Fatalf("concurrent streams share run id %q", r)
		}
		seen[r] = true
	}
}

// TestRequestIDAssigned proves a request without an id still gets a non-empty
// correlation id in its response — never an ambiguous empty one.
func TestRequestIDAssigned(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	resp, err := http.DefaultClient.Do(authed(t, "POST", srv.URL+"/v1/run", `{"v":1,"op":"test.echo","args":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v op.Response
	json.NewDecoder(resp.Body).Decode(&v)
	if v.ID == "" {
		t.Error("daemon did not assign a request id")
	}
	// A client-supplied id is preserved.
	resp2, err := http.DefaultClient.Do(authed(t, "POST", srv.URL+"/v1/run", `{"v":1,"id":"cli-7","op":"test.echo","args":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var v2 op.Response
	json.NewDecoder(resp2.Body).Decode(&v2)
	if v2.ID != "cli-7" {
		t.Errorf("client id not preserved: %q", v2.ID)
	}
	// Streamed events also carry a non-empty id.
	evs := streamEvents(t, srv.URL, `{"v":1,"op":"test.stream","args":{}}`)
	for _, ev := range evs {
		if ev.ID == "" {
			t.Fatalf("event %v has an empty request id", ev.Ev)
		}
	}
}

// TestLifetimeLockSingleton proves only one daemon may hold ownership while a
// live owner exists, ownership frees on release, and a stale (dead-owner) lock
// is recovered.
func TestLifetimeLockSingleton(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	release, err := acquireLifetimeLock()
	if err != nil {
		t.Fatal(err)
	}
	// A second acquirer sees a live owner and fails.
	if _, err := acquireLifetimeLock(); err == nil {
		t.Fatal("second daemon acquired the lifetime lock while owner is alive")
	}
	// Releasing frees ownership.
	release()
	release2, err := acquireLifetimeLock()
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()

	// A stale lock whose owner pid is dead is reclaimed.
	path, err := OwnedLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDeadOwner(path); err != nil {
		t.Fatal(err)
	}
	release3, err := acquireLifetimeLock()
	if err != nil {
		t.Fatalf("stale lock not recovered: %v", err)
	}
	release3()
}

// writeDeadOwner plants a lifetime lock whose recorded owner pid is dead, so a
// test can prove stale-ownership recovery.
func writeDeadOwner(path string) error {
	return os.WriteFile(path, []byte("4194303\n"), 0o600)
}

// TestNextIDMonotonic is a sanity check that the daemon's id sequence never
// repeats, which is what makes run ids unique.
func TestNextIDMonotonic(t *testing.T) {
	s := NewServer(nil, nil, nil)
	a := s.nextID("r")
	b := s.nextID("r")
	if a == b {
		t.Fatalf("nextID repeated %q", a)
	}
}
