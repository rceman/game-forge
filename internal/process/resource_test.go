package process

import (
	"testing"
	"time"
)

func TestRegistryRoundTrip(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res := &Resource{
		ID:        "browser-1",
		RunID:     "run-1",
		Kind:      KindBrowser,
		Provider:  "agent-browser",
		Namespace: "game-forge-run-1",
		Expires:   time.Now().UTC().Add(time.Minute),
	}
	if err := reg.Register(res); err != nil {
		t.Fatalf("Register: %v", err)
	}
	all, err := reg.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != "browser-1" {
		t.Fatalf("List = %+v", all)
	}
	got, err := reg.Get("browser-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != "game-forge-run-1" {
		t.Errorf("namespace = %q", got.Namespace)
	}
	if got.Created.IsZero() {
		t.Error("Created should be defaulted")
	}
	if err := reg.Remove("browser-1"); err != nil {
		t.Fatal(err)
	}
	if all, _ := reg.List(); len(all) != 0 {
		t.Errorf("List after remove = %+v", all)
	}
}

func TestRegisterRequiresIdentity(t *testing.T) {
	reg, _ := Open(t.TempDir())
	for _, res := range []*Resource{
		{Kind: KindBrowser, Provider: "agent-browser"},
		{ID: "x", Provider: "agent-browser"},
		{ID: "x", Kind: KindBrowser},
	} {
		if err := reg.Register(res); err == nil {
			t.Errorf("Register(%+v) = nil error, want error", res)
		}
	}
}

func TestExpired(t *testing.T) {
	reg, _ := Open(t.TempDir())
	now := time.Now().UTC()
	_ = reg.Register(&Resource{ID: "old", Kind: KindBrowser, Provider: "p", Expires: now.Add(-time.Minute)})
	_ = reg.Register(&Resource{ID: "fresh", Kind: KindBrowser, Provider: "p", Expires: now.Add(time.Minute)})
	_ = reg.Register(&Resource{ID: "no-lease", Kind: KindBrowser, Provider: "p"})

	expired, err := reg.Expired(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].ID != "old" {
		t.Fatalf("Expired = %+v, want [old]", expired)
	}
}

func TestRemoveMissingIsNoop(t *testing.T) {
	reg, _ := Open(t.TempDir())
	if err := reg.Remove("absent"); err != nil {
		t.Fatalf("Remove(absent) = %v, want nil", err)
	}
}
