// Package uml runs Linux guests as user-mode Linux: the guest kernel is an
// ordinary process, so it needs neither KVM nor CPU emulation. Guest userland
// runs at native speed; only system calls pay a ptrace tax. Linux guests only.
package uml

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/KVM-X/KVM-X/internal/sys"
)

// Find returns the UML kernel executable, honouring $KVMX_UML.
func Find(s sys.System) string {
	if p := s.Getenv("KVMX_UML"); p != "" {
		return p
	}
	for _, p := range s.LookPathAll("linux.uml") {
		return p
	}
	return ""
}

// TmpDir picks a directory for UML's guest-memory file. UML mmaps it
// executable, so it must be a tmpfs/ramfs that is not mounted noexec; Docker's
// default /dev/shm is noexec, which is why containers need --tmpfs /tmp:exec.
func TmpDir(s sys.System) (string, error) {
	mi, _ := s.ReadFile("/proc/self/mountinfo")
	cands := []string{s.Getenv("TMPDIR"), "/dev/shm", fmt.Sprintf("/run/user/%d", s.UID()), "/tmp", "/var/tmp"}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st := s.Stat(c); !st.Exists || !st.Mode.IsDir() {
			continue
		}
		if ok, _ := execTmpfs(string(mi), c); ok {
			return c, nil
		}
	}
	return "", errors.New("no executable tmpfs for UML's memory file (a tmpfs mounted without noexec is required; in Docker use --tmpfs /tmp:exec)")
}

// execTmpfs reports whether dir lives on a tmpfs/ramfs without noexec.
func execTmpfs(mountinfo, dir string) (bool, string) {
	best, bestOpts, bestFS := "", "", ""
	for _, line := range strings.Split(mountinfo, "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		f := strings.Fields(pre)
		pf := strings.Fields(post)
		if len(f) < 6 || len(pf) < 1 {
			continue
		}
		mp := f[4]
		if (dir == mp || strings.HasPrefix(dir, strings.TrimRight(mp, "/")+"/") || mp == "/") && len(mp) >= len(best) {
			best, bestOpts, bestFS = mp, f[5], pf[0]
		}
	}
	if best == "" {
		return false, ""
	}
	ok := (bestFS == "tmpfs" || bestFS == "ramfs") && !strings.Contains(","+bestOpts+",", ",noexec,")
	return ok, best
}

// Args builds the UML command line for a host-directory root filesystem.
func Args(rootfs, init string, memMB int, extra ...string) []string {
	a := []string{
		fmt.Sprintf("mem=%dM", memMB), "root=/dev/root", "rootfstype=hostfs", "rootflags=" + rootfs, "rw",
		"init=" + init, "con0=fd:0,fd:1", "con=null", "ssl=null", "quiet",
	}
	return append(a, extra...)
}

// Environment returns the environment UML needs (TMPDIR on an exec tmpfs).
func Environment(tmp string) []string { return append(os.Environ(), "TMPDIR="+tmp) }
