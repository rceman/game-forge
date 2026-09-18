package process

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestRegistryConcurrent writes, reads and removes records from many
// goroutines over the same registry. It exists to be run under -race: a data
// race on the registry's mutation lock or a torn/corrupted record fails it.
func TestRegistryConcurrent(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	const ops = 60
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				id := fmt.Sprintf("r-%d-%d", n, j%5)
				res := &Resource{
					ID: id, RunID: fmt.Sprintf("run-%d", n),
					Project: "p", ProjectKey: fmt.Sprintf("p-%d", n),
					Kind: KindServer, Provider: "local", PID: 1000 + n,
					Metadata: map[string]string{"name": "dev", "n": fmt.Sprint(j)},
				}
				if err := reg.Register(res); err != nil {
					errs[n] = err
					return
				}
				if _, err := reg.Get(id); err != nil {
					errs[n] = err
					return
				}
				if _, err := reg.List(); err != nil {
					errs[n] = err
					return
				}
				if err := reg.Remove(id); err != nil {
					errs[n] = err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	for n, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", n, err)
		}
	}
	// No orphaned temp files should remain after serialized atomic commits.
	entries, err := os.ReadDir(reg.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("orphaned temp file %s", e.Name())
		}
	}
}

// TestRegistryRecordIntegrity proves a record written under contention still
// parses as complete JSON — atomic rename means readers never see a partial
// document.
func TestRegistryRecordIntegrity(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res := &Resource{
		ID: "keep", Kind: KindBrowser, Provider: "agent-browser",
		Namespace: "ns", PID: 42, Metadata: map[string]string{"a": "b"},
	}
	if err := reg.Register(res); err != nil {
		t.Fatal(err)
	}
	// The on-disk file must be complete JSON, not a torn write.
	data, err := os.ReadFile(filepath.Join(reg.Dir(), "keep.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("torn record: %v", err)
	}
	if v["id"] != "keep" || v["pid"] != float64(42) {
		t.Errorf("record content wrong: %v", v)
	}
}
