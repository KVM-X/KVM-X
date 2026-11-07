package guest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCPIORoundTrip(t *testing.T) {
	if _, err := exec.LookPath("cpio"); err != nil {
		t.Skip("cpio not installed")
	}
	data := CPIO([]CPIOEntry{{Name: "init", Mode: 0o100755, Data: []byte(InitScript)}})
	dir := t.TempDir()
	cmd := exec.Command("cpio", "-idm", "--quiet")
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(data)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cpio: %v %s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "init"))
	if err != nil || string(got) != InitScript {
		t.Fatalf("init mismatch: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "init")); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", fi.Mode())
	}
}

func TestOverlayAlignsAndAppends(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	_ = os.WriteFile(base, []byte("\x1f\x8bXXXXX"), 0o644) // 7 bytes: needs padding
	out, err := Overlay(base, InitScript, dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	i := bytes.Index(b, []byte("070701"))
	if i%4 != 0 || i < 7 {
		t.Errorf("cpio starts at %d, not 4-aligned after base", i)
	}
	if !strings.HasSuffix(string(b), "\x00") && !bytes.Contains(b, []byte("TRAILER!!!")) {
		t.Error("trailer missing")
	}
}

func TestAssetsPinned(t *testing.T) {
	for _, arch := range []string{"x86_64", "aarch64"} {
		as, err := Assets(arch)
		if err != nil || len(as) != 2 {
			t.Fatalf("%s: %v", arch, err)
		}
		for _, a := range as {
			if len(a.SHA256) != 64 || !strings.HasPrefix(a.URL, "https://dl-cdn.alpinelinux.org/alpine/v3.24/") {
				t.Errorf("bad asset %+v", a)
			}
		}
	}
	if _, err := Assets("riscv64"); err == nil {
		t.Error("expected error for unsupported arch")
	}
}
