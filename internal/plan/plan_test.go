package plan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KVM-X/KVM-X/internal/detect"
	"github.com/KVM-X/KVM-X/internal/journal"
	"github.com/KVM-X/KVM-X/internal/ops"
	"github.com/KVM-X/KVM-X/internal/plan"
	"github.com/KVM-X/KVM-X/internal/sys"
)

func build(t *testing.T, name string, o plan.Options) (*plan.Plan, *detect.Report) {
	t.Helper()
	f, err := sys.LoadFixture(filepath.Join("../../testdata/fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	r := detect.Collect(f, detect.Options{})
	return plan.Build(f, r, o), r
}

func ids(p *plan.Plan) string {
	var s []string
	for _, st := range p.Steps {
		s = append(s, st.ID)
	}
	return strings.Join(s, ",")
}

func TestBuildSteps(t *testing.T) {
	cases := []struct {
		fixture string
		opts    plan.Options
		want    string
		expect  detect.Mode
	}{
		{"baremetal-intel-kvm-ok", plan.Options{}, "", detect.ModeHardware},
		{"baremetal-amd-no-kvm-group", plan.Options{}, "kvm-access", detect.ModeTCG}, // expected set below
		{"baremetal-intel-module-not-loaded", plan.Options{}, "nested-modprobe-conf,load-kvm-module", detect.ModeTCG},
		{"baremetal-nested-off", plan.Options{}, "nested-modprobe-conf,reload-kvm-module", detect.ModeHardware},
		{"kvm-guest-dev-kvm-missing", plan.Options{}, "mknod-dev-kvm,kvm-access", detect.ModeTCG},
		{"kvm-ok-qemu-missing", plan.Options{}, "install-qemu", detect.ModeHardware},
		{"gcp-no-nested", plan.Options{Binary: "/usr/local/bin/kvmx"}, "install-shims", detect.ModeTCG},
		{"aws-nitro-no-nested", plan.Options{Binary: "/usr/local/bin/kvmx", Shims: "no"}, "", detect.ModeTCG},
		{"kvm-guest-nested-ok", plan.Options{Binary: "/usr/local/bin/kvmx", Persist: true}, "systemd-persist", detect.ModeNested},
		{"baremetal-intel-module-not-loaded", plan.Options{Boot: true, NoNested: true}, "load-kvm-module", detect.ModeTCG},
	}
	for _, c := range cases {
		p, _ := build(t, c.fixture, c.opts)
		if got := ids(p); got != c.want {
			t.Errorf("%s: steps = %q, want %q", c.fixture, got, c.want)
		}
	}
}

func TestManualGuidance(t *testing.T) {
	p, _ := build(t, "aws-nitro-no-nested", plan.Options{})
	if len(p.Manual) == 0 || !strings.Contains(strings.Join(p.Manual[0].Steps, "\n"), "nested-virtualization") {
		t.Errorf("aws guidance missing: %+v", p.Manual)
	}
	p, _ = build(t, "docker-no-kvm-device", plan.Options{})
	if len(p.Manual) == 0 || !strings.Contains(strings.Join(p.Manual[0].Steps, "\n"), "--device /dev/kvm") {
		t.Errorf("docker guidance missing: %+v", p.Manual)
	}
}

func TestInstallQEMUDistros(t *testing.T) {
	p, _ := build(t, "kvm-ok-qemu-missing", plan.Options{})
	var b strings.Builder
	p.Print(&b, true)
	if !strings.Contains(b.String(), "sudo env DEBIAN_FRONTEND=noninteractive apt-get install") || !strings.Contains(b.String(), "qemu-system-x86") {
		t.Errorf("unexpected plan text:\n%s", b.String())
	}
}

func TestUnitMatchesPackagingCopy(t *testing.T) {
	b, err := os.ReadFile("../../packaging/systemd/kvmx.service")
	if err != nil {
		t.Skip("packaging copy not present yet")
	}
	if string(b) != plan.Unit("/usr/local/bin/kvmx") {
		t.Error("packaging/systemd/kvmx.service drifted from plan.Unit; regenerate with `kvmx systemd-unit`")
	}
}

type fakeRunner struct{ calls [][]string }

func (f *fakeRunner) Run(argv []string, priv bool) (string, error) {
	f.calls = append(f.calls, argv)
	return "", nil
}

func TestExecuteAndRevert(t *testing.T) {
	root := t.TempDir()
	fr := &fakeRunner{}
	env := &ops.Env{Root: root, Runner: fr}
	j := &journal.Journal{Dir: t.TempDir()}
	p, _ := build(t, "baremetal-intel-module-not-loaded", plan.Options{})

	var out strings.Builder
	if failed := plan.Execute(p, env, j, &out); failed != 0 {
		t.Fatalf("failed steps: %d\n%s", failed, out.String())
	}
	conf, err := os.ReadFile(filepath.Join(root, "/etc/modprobe.d/kvmx.conf"))
	if err != nil || !strings.Contains(string(conf), "options kvm_intel nested=1") {
		t.Fatalf("modprobe conf: %q %v", conf, err)
	}
	if len(fr.calls) == 0 || strings.Join(fr.calls[0], " ") != "modprobe kvm_intel" {
		t.Fatalf("calls = %v", fr.calls)
	}
	entries, _ := j.Load()
	if len(entries) < 2 {
		t.Fatalf("journal entries = %d", len(entries))
	}

	// Second run is a no-op for file ops (idempotent).
	before, _ := j.Load()
	plan.Execute(p, env, j, &out)
	after, _ := j.Load()
	for _, e := range after[len(before):] {
		if e.Undo.Kind == "remove-file" || e.Undo.Kind == "restore-file" {
			t.Errorf("re-apply journaled a file change: %+v", e)
		}
	}

	// Revert in reverse order removes files we created.
	for i := len(after) - 1; i >= 0; i-- {
		if _, err := after[i].Undo.Do(env, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "/etc/modprobe.d/kvmx.conf")); !os.IsNotExist(err) {
		t.Error("modprobe conf survived revert")
	}
}

func TestRestoreFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "etc/modprobe.d/kvmx.conf")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte("original\n"), 0o600)
	env := &ops.Env{Root: root, Runner: &fakeRunner{}}
	undo, err := ops.WriteFile{Path: "/etc/modprobe.d/kvmx.conf", Content: "new\n", Mode: 0o644}.Apply(env)
	if err != nil || undo.Kind != "restore-file" {
		t.Fatalf("undo=%+v err=%v", undo, err)
	}
	if _, err := undo.Do(env, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "original\n" {
		t.Errorf("not restored: %q", b)
	}
}

func TestSymlinkRefusesToClobber(t *testing.T) {
	root := t.TempDir()
	env := &ops.Env{Root: root, Runner: &fakeRunner{}}
	_ = os.MkdirAll(filepath.Join(root, "usr/local/bin"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "usr/local/bin/kvm"), []byte("#!/bin/sh\n"), 0o755)
	if _, err := (ops.Symlink{Path: "/usr/local/bin/kvm", Target: "/x/kvmx"}).Apply(env); err == nil {
		t.Error("expected refusal to replace a regular file")
	}
}
