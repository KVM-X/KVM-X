package detect_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/KVM-X/KVM-X/internal/detect"
	"github.com/KVM-X/KVM-X/internal/sys"
)

type expect struct {
	Mode         string   `json:"mode"`
	Potential    string   `json:"potential"`
	Blockers     []string `json:"blockers"`
	Ready        bool     `json:"ready"`
	Provider     string   `json:"provider"`
	Container    string   `json:"container"`
	InstanceType string   `json:"instance_type"`
	NoteContains string   `json:"note_contains"`
}

const fixtureDir = "../../testdata/fixtures"

func TestFixtures(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join(fixtureDir, "*", "meta.json"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no fixtures found in %s (run python3 testdata/gen_fixtures.py): %v", fixtureDir, err)
	}
	for _, m := range dirs {
		dir := filepath.Dir(m)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			f, err := sys.LoadFixture(dir)
			if err != nil {
				t.Fatal(err)
			}
			var want expect
			b, err := os.ReadFile(filepath.Join(dir, "expect.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			r := detect.Collect(f, detect.Options{Version: "test", Metadata: true})

			if string(r.Mode) != want.Mode {
				t.Errorf("mode = %q, want %q", r.Mode, want.Mode)
			}
			if string(r.Potential) != want.Potential {
				t.Errorf("potential = %q, want %q", r.Potential, want.Potential)
			}
			if r.Ready != want.Ready {
				t.Errorf("ready = %v, want %v (%s)", r.Ready, want.Ready, r.ReadyNote)
			}
			got := []string{}
			for _, bl := range r.Blockers {
				got = append(got, bl.ID)
			}
			sort.Strings(got)
			wantB := append([]string{}, want.Blockers...)
			sort.Strings(wantB)
			if strings.Join(got, ",") != strings.Join(wantB, ",") {
				t.Errorf("blockers = %v, want %v", got, wantB)
			}
			if want.Provider != "" && r.Provider.ID != want.Provider {
				t.Errorf("provider = %q, want %q", r.Provider.ID, want.Provider)
			}
			if want.Container != "" && r.Virt.ContainerType != want.Container {
				t.Errorf("container = %q, want %q", r.Virt.ContainerType, want.Container)
			}
			if want.InstanceType != "" && r.Provider.InstanceType != want.InstanceType {
				t.Errorf("instance type = %q, want %q", r.Provider.InstanceType, want.InstanceType)
			}
			if want.NoteContains != "" && !strings.Contains(strings.Join(r.Notes, "\n"), want.NoteContains) {
				t.Errorf("notes %q do not mention %q", r.Notes, want.NoteContains)
			}
			// Invariant: software emulation is never reported as KVM.
			if !r.KVM.Usable && r.Mode != detect.ModeTCG {
				t.Errorf("unusable KVM reported as %s", r.Mode)
			}
		})
	}
}

func TestModeStrings(t *testing.T) {
	for m, code := range map[detect.Mode]string{
		detect.ModeHardware: "hardware-kvm", detect.ModeNested: "nested-kvm", detect.ModeTCG: "software-tcg",
	} {
		if m.Code() != code || m.Penalty() == "" {
			t.Errorf("%s: code %q penalty %q", m, m.Code(), m.Penalty())
		}
	}
}

// A recorded run must replay to the identical verdict (this is what `kvmx collect` relies on).
func TestRecorderRoundTrip(t *testing.T) {
	for _, name := range []string{"baremetal-amd-no-kvm-group", "gcp-no-nested", "docker-no-kvm-device", "arm64-baremetal-kvm-ok"} {
		src, err := sys.LoadFixture(filepath.Join(fixtureDir, name))
		if err != nil {
			t.Fatal(err)
		}
		rec := sys.NewRecorder(src)
		want := detect.Collect(rec, detect.Options{Metadata: true})
		dir := t.TempDir()
		if err := rec.Save(dir); err != nil {
			t.Fatal(err)
		}
		replay, err := sys.LoadFixture(dir)
		if err != nil {
			t.Fatal(err)
		}
		got := detect.Collect(replay, detect.Options{Metadata: true})
		if got.Mode != want.Mode || got.Potential != want.Potential || len(got.Blockers) != len(want.Blockers) ||
			got.Provider.ID != want.Provider.ID || got.Ready != want.Ready {
			t.Errorf("%s: replay differs:\n got  %+v\n want %+v", name, got.Blockers, want.Blockers)
		}
	}
}
