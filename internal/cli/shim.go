package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/KVM-X/KVM-X/internal/qemu"
	"github.com/KVM-X/KVM-X/internal/sys"
)

// shim lets one binary stand in for qemu-system-*, kvm, virsh and virt-install.
// When KVM works the real tool runs untouched; when it does not, KVM-only
// options are rewritten so the same command line runs under tuned TCG.
func (a *app) shim(args []string) (int, bool) {
	base := filepath.Base(args[0])
	switch {
	case strings.HasPrefix(base, "qemu-system-"), base == "kvm", base == "qemu-kvm":
		return a.shimQEMU(base, args[1:]), true
	case base == "virsh", base == "virt-install":
		return a.shimLibvirt(base, args[1:]), true
	}
	return 0, false
}

func kvmUsable() bool {
	h := sys.Host{}
	if os.Getenv("KVMX_FORCE_TCG") == "1" || !h.Stat("/dev/kvm").Exists {
		return false
	}
	p := h.ProbeKVM("/dev/kvm")
	return p.Opened && p.APIVersion == 12 && p.CreateVMOK
}

func shimProfile() qemu.Profile {
	if p, ok := qemu.Profiles[os.Getenv("KVMX_PROFILE")]; ok {
		return p
	}
	return qemu.Profiles[qemu.DefaultProfile]
}

// realTool finds name on PATH, skipping kvmx shims and this very binary.
func realTool(names ...string) string {
	self, _ := os.Stat(selfPath())
	for _, n := range names {
		for _, p := range (sys.Host{}).LookPathAll(n) {
			if sys.IsShim(p) {
				continue
			}
			if fi, err := os.Stat(p); err == nil && self != nil && os.SameFile(fi, self) {
				continue
			}
			return p
		}
	}
	return ""
}

func (a *app) notice(format string, args ...any) {
	if os.Getenv("KVMX_QUIET") != "1" {
		fmt.Fprintf(a.err, "kvmx: "+format+"\n", args...)
	}
}

func (a *app) shimQEMU(base string, args []string) int {
	names := []string{base}
	if base == "qemu-system-"+archName() {
		names = append(names, "qemu-kvm") // RHEL family ships only qemu-kvm
	}
	if base == "kvm" || base == "qemu-kvm" {
		names = []string{"qemu-system-x86_64", "qemu-kvm"}
		args = append([]string{"-enable-kvm"}, args...) // legacy `kvm` always meant -enable-kvm
	}
	real := realTool(names...)
	if real == "" {
		fmt.Fprintf(a.err, "kvmx shim: the real %s was not found on PATH (is QEMU installed? run `kvmx enable`)\n", names[0])
		return 127
	}
	if !kvmUsable() {
		var changes []string
		args, changes = qemu.RewriteForTCG(args, shimProfile())
		a.notice("KVM unavailable - running under SOFTWARE EMULATION (TCG), expect 5-30x slower CPU-bound performance")
		if os.Getenv("KVMX_VERBOSE") == "1" {
			for _, c := range changes {
				a.notice("  %s", c)
			}
		}
	}
	return execReal(real, args)
}

func execReal(path string, args []string) int {
	argv := append([]string{path}, args...)
	err := syscall.Exec(path, argv, os.Environ()) //nolint:gosec // path was resolved from PATH by realTool
	fmt.Fprintf(os.Stderr, "kvmx shim: exec %s: %v\n", path, err)
	return 126
}

func (a *app) shimLibvirt(base string, args []string) int {
	real := realTool(base)
	if real == "" {
		fmt.Fprintf(a.err, "kvmx shim: the real %s was not found on PATH\n", base)
		return 127
	}
	if kvmUsable() || remoteURI(args) {
		return execReal(real, args)
	}
	out := append([]string{}, args...)
	switch base {
	case "virt-install":
		out = rewriteVirtInstall(out)
		a.notice("KVM unavailable - virt-install will create a TCG (software emulation) guest")
	case "virsh":
		for i, x := range out {
			if (x == "define" || x == "create") && i+1 < len(out) {
				j := i + 1
				for j < len(out) && strings.HasPrefix(out[j], "-") {
					j++
				}
				if j < len(out) {
					if p, ok := a.rewriteDomainFile(out[j]); ok {
						out[j] = p
					}
				}
				break
			}
		}
	}
	return execReal(real, out)
}

func remoteURI(args []string) bool {
	for i, x := range args {
		uri := ""
		switch {
		case (x == "-c" || x == "--connect") && i+1 < len(args):
			uri = args[i+1]
		case strings.HasPrefix(x, "--connect="):
			uri = strings.TrimPrefix(x, "--connect=")
		}
		if strings.Contains(uri, "+") || strings.Contains(uri, "://") && !strings.Contains(uri, ":///") {
			return true
		}
	}
	return false
}

func (a *app) rewriteDomainFile(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	x, changes := qemu.RewriteDomainXML(string(b))
	if len(changes) == 0 {
		return "", false
	}
	tmp, err := os.CreateTemp("", "kvmx-domain-*.xml")
	if err != nil {
		return "", false
	}
	_, _ = tmp.WriteString(x)
	_ = tmp.Close()
	a.notice("KVM unavailable - rewrote %s for software emulation: %s", path, strings.Join(changes, "; "))
	return tmp.Name(), true
}

func rewriteVirtInstall(args []string) []string {
	var out []string
	hasType := false
	for i := 0; i < len(args); i++ {
		x := args[i]
		switch {
		case x == "--virt-type" && i+1 < len(args):
			out = append(out, x, "qemu")
			i++
			hasType = true
		case strings.HasPrefix(x, "--virt-type="):
			out = append(out, "--virt-type=qemu")
			hasType = true
		case x == "--cpu" && i+1 < len(args):
			out = append(out, x, cpuForVirtInstall(args[i+1]))
			i++
		case strings.HasPrefix(x, "--cpu="):
			out = append(out, "--cpu="+cpuForVirtInstall(strings.TrimPrefix(x, "--cpu=")))
		default:
			out = append(out, x)
		}
	}
	if !hasType {
		out = append(out, "--virt-type", "qemu")
	}
	return out
}

func cpuForVirtInstall(v string) string {
	if strings.HasPrefix(v, "host") {
		return "max"
	}
	return v
}

func archName() string {
	return map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
}
