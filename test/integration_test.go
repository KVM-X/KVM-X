//go:build integration

// Package test holds end-to-end tests: real TCG boots, shims, Docker, and a
// matrix of distro containers. Run with `make integration`.
package test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/KVM-X/KVM-X/internal/guest"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	return filepath.Dir(wd)
}

func kvmx(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("KVMX_BIN"); b != "" {
		return b
	}
	return filepath.Join(repoRoot(t), "bin", "kvmx")
}

func cacheDir(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("KVMX_CACHE_DIR"); d != "" {
		return d
	}
	d := filepath.Join(os.TempDir(), "kvmx-it-cache")
	t.Setenv("KVMX_CACHE_DIR", d)
	return d
}

type result struct {
	out, err string
	code     int
}

func run(t *testing.T, env []string, name string, args ...string) result {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return result{o.String(), e.String(), code}
}

func fixture(t *testing.T, name string) string {
	return filepath.Join(repoRoot(t), "testdata", "fixtures", name)
}

func TestDetectJSON(t *testing.T) {
	r := run(t, nil, kvmx(t), "detect", "--json")
	var rep struct {
		Schema int    `json:"schema_version"`
		Mode   string `json:"mode"`
		Code   string `json:"mode_code"`
	}
	if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, r.out)
	}
	if rep.Schema != 1 || rep.Code == "" {
		t.Errorf("unexpected report: %+v", rep)
	}
	switch rep.Mode {
	case "HARDWARE-KVM", "NESTED-KVM", "SOFTWARE-EMULATION (TCG)":
	default:
		t.Errorf("mode %q is not one of the three allowed statuses", rep.Mode)
	}
}

func TestStatusExitCodesOnFixtures(t *testing.T) {
	for fx, want := range map[string]int{"baremetal-intel-kvm-ok": 0, "kvm-guest-nested-ok": 0, "gcp-no-nested": 10, "oracle-vm-no-virt": 20} {
		if r := run(t, nil, kvmx(t), "status", "--fixture", fixture(t, fx)); r.code != want {
			t.Errorf("%s: exit %d, want %d (%s)", fx, r.code, want, r.out)
		}
	}
}

func TestEnableDryRunChangesNothing(t *testing.T) {
	state := t.TempDir()
	r := run(t, []string{"KVMX_STATE_DIR=" + state}, kvmx(t), "enable", "--dry-run", "--json", "--fixture", fixture(t, "baremetal-amd-no-kvm-group"))
	if r.code != 0 {
		t.Fatalf("exit %d: %s %s", r.code, r.out, r.err)
	}
	var res struct {
		Applied bool `json:"applied"`
		Plan    []struct {
			ID string `json:"id"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(r.out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Applied || len(res.Plan) == 0 || res.Plan[0].ID != "kvm-access" {
		t.Errorf("unexpected dry-run result: %+v", res)
	}
	if es, _ := os.ReadDir(state); len(es) != 0 {
		t.Errorf("dry-run wrote state: %v", es)
	}
}

// demoKernel makes sure the pinned Alpine guest is cached and returns the paths.
func demoKernel(t *testing.T) (kernel, initrd string) {
	t.Helper()
	cacheDir(t)
	p, err := guest.Ensure(map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH],
		guest.EnsureOpts{Yes: true, Out: os.Stderr})
	if err != nil {
		t.Fatalf("test guest: %v", err)
	}
	img, err := guest.Overlay(p.Initrd, guest.InitScript, filepath.Join(guest.CacheDir(), "guest", "overlay"))
	if err != nil {
		t.Fatal(err)
	}
	return p.Kernel, img
}

type runJSON struct {
	Mode       string   `json:"mode"`
	ExitCode   int      `json:"exit_code"`
	ElapsedMS  int64    `json:"elapsed_ms"`
	Matched    bool     `json:"expect_matched"`
	ExpectMS   int64    `json:"expect_ms"`
	GuestBench []string `json:"guest_bench"`
}

// The headline proof: boot a real Linux guest under software emulation and record how long it took.
func TestTCGBootsRealGuest(t *testing.T) {
	demoKernel(t)
	r := run(t, nil, kvmx(t), "run", "--demo", "--mode", "tcg", "--yes", "--guest-bench", "--json", "--timeout", "5m")
	var rr runJSON
	if err := json.Unmarshal([]byte(r.out), &rr); err != nil {
		t.Fatalf("bad JSON: %v\n%s\n%s", err, r.out, r.err)
	}
	if rr.Mode != "SOFTWARE-EMULATION (TCG)" || !rr.Matched {
		t.Fatalf("guest did not boot under TCG: %+v\nstderr: %s", rr, r.err)
	}
	t.Logf("TCG: guest finished in %d ms (%v)", rr.ElapsedMS, rr.GuestBench)
	if out := os.Getenv("KVMX_RESULTS"); out != "" {
		b, _ := json.MarshalIndent(map[string]any{
			"recorded": time.Now().UTC().Format(time.RFC3339), "mode": rr.Mode, "guest": "Alpine " + guest.AlpineVersion + " linux-virt",
			"total_ms": rr.ElapsedMS, "guest_bench": rr.GuestBench, "host": runtime.GOOS + "/" + runtime.GOARCH,
		}, "", "  ")
		_ = os.WriteFile(out, append(b, '\n'), 0o644)
	}
}

func TestRunModeKVMRefusesToFallBack(t *testing.T) {
	r := run(t, nil, kvmx(t), "run", "--mode", "kvm", "--demo", "--dry-run", "--fixture", fixture(t, "gcp-no-nested"))
	if r.code == 0 || !strings.Contains(r.err, "--mode kvm requested") {
		t.Errorf("--mode kvm must refuse when KVM is unusable; exit %d stderr %q", r.code, r.err)
	}
}

// KVM-style qemu command lines keep working through the shim when KVM is unavailable.
func TestQEMUShimRewritesToTCG(t *testing.T) {
	kernel, initrd := demoKernel(t)
	shimDir := t.TempDir()
	if err := os.Symlink(kvmx(t), filepath.Join(shimDir, "qemu-system-x86_64")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOARCH != "amd64" {
		t.Skip("shim boot test uses x86_64")
	}
	r := run(t, []string{"PATH=" + shimDir + ":" + os.Getenv("PATH"), "KVMX_FORCE_TCG=1", "KVMX_VERBOSE=1"},
		"timeout", "300", "qemu-system-x86_64", "-enable-kvm", "-cpu", "host", "-m", "512", "-smp", "2", "-nographic", "-no-reboot",
		"-kernel", kernel, "-initrd", initrd, "-append", "console=ttyS0")
	if !strings.Contains(r.out, "KVMX-DONE") {
		t.Fatalf("guest did not boot via shim (exit %d)\nstdout tail: %.400s\nstderr: %s", r.code, tail(r.out), r.err)
	}
	for _, want := range []string{"SOFTWARE EMULATION", "-enable-kvm", "-cpu host -> max"} {
		if !strings.Contains(r.err, want) {
			t.Errorf("shim stderr lacks %q:\n%s", want, r.err)
		}
	}
}

func tail(s string) string {
	if len(s) > 400 {
		return s[len(s)-400:]
	}
	return s
}

func TestVirshShimRewritesDomain(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	shimDir := filepath.Join(dir, "shim")
	for _, d := range []string{realDir, shimDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A fake virsh that prints the domain file it receives.
	script := "#!/bin/sh\nfor a; do last=$a; done\ncat \"$last\"\n"
	if err := os.WriteFile(filepath.Join(realDir, "virsh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(kvmx(t), filepath.Join(shimDir, "virsh")); err != nil {
		t.Fatal(err)
	}
	xml := filepath.Join(dir, "vm.xml")
	_ = os.WriteFile(xml, []byte("<domain type='kvm'><name>t</name><cpu mode='host-passthrough'/></domain>"), 0o644)
	r := run(t, []string{"PATH=" + shimDir + ":" + realDir + ":" + os.Getenv("PATH"), "KVMX_FORCE_TCG=1"}, filepath.Join(shimDir, "virsh"), "define", xml)
	if !strings.Contains(r.out, "<domain type='qemu'>") || strings.Contains(r.out, "host-passthrough") {
		t.Errorf("virsh shim did not rewrite the domain:\n%s\n%s", r.out, r.err)
	}
}

func hasDocker() bool {
	if os.Getenv("KVMX_SKIP_DOCKER") == "1" {
		return false
	}
	return exec.Command("docker", "info").Run() == nil
}

func TestDockerImage(t *testing.T) {
	if !hasDocker() {
		t.Skip("docker not available")
	}
	cache := cacheDir(t)
	demoKernel(t)
	root := repoRoot(t)
	if r := run(t, nil, "docker", "build", "-q", "-t", "kvmx:it", root); r.code != 0 {
		t.Fatalf("docker build: %s %s", r.out, r.err)
	}
	// No /dev/kvm in the container: honest software emulation.
	r := run(t, nil, "docker", "run", "--rm", "kvmx:it", "status", "--json")
	if r.code != 10 || !strings.Contains(r.out, `"mode": "SOFTWARE-EMULATION (TCG)"`) {
		t.Errorf("container without /dev/kvm: exit %d\n%s", r.code, r.out)
	}
	r = run(t, nil, "docker", "run", "--rm", "-v", cache+":/var/cache/kvmx", "kvmx:it", "run", "--demo", "--yes", "--mode", "tcg", "--json", "--timeout", "5m")
	var rr runJSON
	if err := json.Unmarshal([]byte(r.out), &rr); err != nil || !rr.Matched {
		t.Fatalf("container TCG boot failed: %v\n%s\n%s", err, r.out, r.err)
	}
	t.Logf("container TCG boot: %d ms", rr.ElapsedMS)

	if _, err := os.Stat("/dev/kvm"); err == nil {
		args := []string{"run", "--rm", "--device", "/dev/kvm", "--group-add", gidOfKVM(), "kvmx:it", "status", "--json"}
		r = run(t, nil, "docker", args...)
		if !strings.Contains(r.out, "KVM") || strings.Contains(r.out, `"mode": "SOFTWARE`) {
			t.Errorf("container with /dev/kvm should reach KVM: exit %d\n%s", r.code, r.out)
		}
	}
}

func gidOfKVM() string {
	out, err := exec.Command("stat", "-c", "%g", "/dev/kvm").Output()
	if err != nil {
		return "0"
	}
	return strings.TrimSpace(string(out))
}

// Every package manager kvmx supports, for real: install QEMU, boot the guest, shim it, revert.
func TestDistroMatrix(t *testing.T) {
	if !hasDocker() || os.Getenv("KVMX_SKIP_DISTROS") == "1" {
		t.Skip("docker unavailable or KVMX_SKIP_DISTROS=1")
	}
	images := map[string]string{ // image -> package manager it exercises
		"alpine:3.24": "apk", "debian:12-slim": "apt-get", "ubuntu:24.04": "apt-get", "fedora:latest": "dnf",
		"rockylinux:9": "dnf", "archlinux:latest": "pacman", "opensuse/tumbleweed:latest": "zypper",
	}
	list := strings.Fields(os.Getenv("KVMX_DISTROS"))
	if len(list) == 0 {
		for img := range images {
			list = append(list, img)
		}
	}
	cache := cacheDir(t)
	demoKernel(t)
	script := filepath.Join(repoRoot(t), "scripts", "e2e-container.sh")
	for _, img := range list {
		t.Run(strings.ReplaceAll(img, "/", "_"), func(t *testing.T) {
			start := time.Now()
			r := run(t, nil, "docker", "run", "--rm", "-v", kvmx(t)+":/usr/local/bin/kvmx:ro", "-v", cache+":/cache",
				"-v", script+":/e2e.sh:ro", "-e", "KVMX_CACHE_DIR=/cache", img, "sh", "/e2e.sh")
			t.Logf("%s (%s): %.0fs", img, images[img], time.Since(start).Seconds())
			if r.code != 0 || !strings.Contains(r.out, "E2E OK") {
				t.Fatalf("exit %d\n--- stdout ---\n%s\n--- stderr ---\n%s", r.code, lastLines(r.out, 40), lastLines(r.err, 20))
			}
		})
	}
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}
