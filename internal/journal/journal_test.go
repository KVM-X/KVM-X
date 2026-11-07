package journal

import (
	"testing"

	"github.com/KVM-X/KVM-X/internal/ops"
)

func TestAppendLoadSave(t *testing.T) {
	j := &Journal{Dir: t.TempDir()}
	if es, err := j.Load(); err != nil || len(es) != 0 {
		t.Fatalf("empty journal: %v %v", es, err)
	}
	for i, step := range []string{"a", "b", "c"} {
		e, err := j.Append(Entry{Step: step, Desc: "d", Undo: ops.Undo{Kind: "remove-file", Path: "/x" + step}})
		if err != nil || e.ID != i+1 {
			t.Fatalf("append %d: id %d err %v", i, e.ID, err)
		}
	}
	es, _ := j.Load()
	if len(es) != 3 || es[2].Undo.Path != "/xc" {
		t.Fatalf("loaded %+v", es)
	}
	if err := j.Save(es[:1]); err != nil {
		t.Fatal(err)
	}
	if es, _ = j.Load(); len(es) != 1 || es[0].Step != "a" {
		t.Fatalf("after save: %+v", es)
	}
	if err := j.Save(nil); err != nil {
		t.Fatal(err)
	}
	if es, _ = j.Load(); len(es) != 0 {
		t.Fatalf("journal should be gone: %+v", es)
	}
}

func TestStateDirEnvOverride(t *testing.T) {
	t.Setenv("KVMX_STATE_DIR", "/custom/state")
	if got := StateDir(); got != "/custom/state" {
		t.Errorf("StateDir = %q", got)
	}
}
