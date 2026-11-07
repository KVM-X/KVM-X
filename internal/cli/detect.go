package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/KVM-X/KVM-X/internal/detect"
	"github.com/KVM-X/KVM-X/internal/guide"
	"github.com/KVM-X/KVM-X/internal/journal"
)

func (a *app) cmdDetect(args []string) int {
	var c common
	fs := a.flagSet("detect", &c)
	if code, ok := parse(fs, args); !ok {
		return code
	}
	_, r, err := a.collect(&c, false)
	if err != nil {
		return a.fail("%v", err)
	}
	if c.json {
		a.emit(r)
		return exitOK
	}
	a.printReport(a.out, r)
	return exitOK
}

func (a *app) printReport(w io.Writer, r *detect.Report) {
	k := func(key, val string) { fmt.Fprintf(w, "  %-10s %s\n", key+":", val) }
	fmt.Fprintf(w, "kvmx %s\n\n", r.Version)
	fmt.Fprintf(w, "  STATUS     %s\n", a.modeStyled(r.Mode))
	fmt.Fprintf(w, "             expected performance: %s\n", r.Penalty)
	if r.Ready {
		k("ready", "yes - kvmx run can start a VM now")
	} else {
		k("ready", "no - "+r.ReadyNote)
	}
	if r.Potential != r.Mode {
		k("potential", string(r.Potential)+" (see blockers below)")
	}
	fmt.Fprintln(w)

	cpu := r.CPU
	k("cpu", fmt.Sprintf("%s (%s, %d logical) virt extension: %s", orDash(cpu.Model), cpu.Arch, cpu.Cores, cpu.VirtExt))
	virt := "bare metal"
	switch {
	case r.Virt.IsVM && r.Virt.IsContainer:
		virt = fmt.Sprintf("%s container inside a %s VM", r.Virt.ContainerType, r.Virt.VMType)
	case r.Virt.IsVM:
		virt = "guest of " + r.Virt.VMType
	case r.Virt.IsContainer:
		virt = r.Virt.ContainerType + " container"
	}
	k("virt", virt)
	prov := r.Provider.Name
	if r.Provider.InstanceType != "" {
		prov += " (" + r.Provider.InstanceType + ")"
	}
	k("provider", prov)

	kv := r.KVM
	mod := kv.Module + " not loaded"
	switch {
	case kv.ModuleLoaded && kv.NestedParam != "":
		mod = fmt.Sprintf("%s loaded, nested=%s", kv.Module, kv.NestedParam)
	case kv.ModuleLoaded:
		mod = kv.Module + " loaded"
	case kv.ModuleAvailable:
		mod += " (available)"
	default:
		mod += " (not in this kernel)"
	}
	k("kvm", mod)
	switch {
	case !kv.DevExists:
		k("/dev/kvm", "missing")
	case kv.Usable:
		k("/dev/kvm", fmt.Sprintf("%s %s - API %d, VM creation OK", kv.DevMode, orDash(kv.DevGroup), kv.Probe.APIVersion))
	default:
		why := kv.Probe.OpenErr
		if why == "" {
			why = kv.Probe.APIErr + kv.Probe.CreateErr
		}
		k("/dev/kvm", fmt.Sprintf("%s %s - not usable: %s", kv.DevMode, orDash(kv.DevGroup), why))
	}

	sb := r.Sandbox
	k("sandbox", fmt.Sprintf("uid %d (%s)  sudo:%s  cap_mknod:%v cap_sys_module:%v seccomp:%d", sb.UID, sb.User, yn(sb.HasSudo), sb.CapMknod, sb.CapSysModule, sb.Seccomp))
	t := r.Tools
	qemu := "not installed"
	if t.QEMU != "" {
		qemu = fmt.Sprintf("%s (%s, tcg:%v)", t.QEMU, t.QEMUVersion, t.TCG)
	}
	k("qemu", qemu)
	k("packages", orDash(t.PkgManager)+"  distro: "+orDash(r.Distro.Pretty))

	if len(r.Blockers) > 0 {
		fmt.Fprintln(w, "\n  blockers:")
		for _, b := range r.Blockers {
			fix := "needs manual action"
			if b.Fixable {
				fix = "fixable by `kvmx enable`"
			}
			fmt.Fprintf(w, "    - %s: %s [%s]\n", b.ID, b.Summary, fix)
			if b.Detail != "" {
				fmt.Fprintf(w, "        %s\n", b.Detail)
			}
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "\n  note: %s", n)
	}
	fmt.Fprintln(w)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (a *app) cmdStatus(args []string) int {
	var c common
	fs := a.flagSet("status", &c)
	if code, ok := parse(fs, args); !ok {
		return code
	}
	_, r, err := a.collect(&c, false)
	if err != nil {
		return a.fail("%v", err)
	}
	j := &journal.Journal{Dir: journal.StateDir()}
	entries, _ := j.Load()
	code := exitOK
	switch {
	case !r.Ready:
		code = exitNotReady
	case r.Mode == detect.ModeTCG:
		code = exitSoftware
	}
	if c.json {
		a.emit(map[string]any{
			"mode": r.Mode, "mode_code": r.ModeCode, "expected_penalty": r.Penalty, "ready": r.Ready,
			"ready_note": r.ReadyNote, "potential_mode": r.Potential, "kvmx_changes_applied": len(entries), "exit_code": code,
		})
		return code
	}
	if !c.quiet {
		line := fmt.Sprintf("%s - %s", a.modeStyled(r.Mode), r.Penalty)
		if !r.Ready {
			line += " [not ready: " + r.ReadyNote + "]"
		}
		fmt.Fprintln(a.out, line)
		if r.Potential != r.Mode {
			fmt.Fprintf(a.out, "reachable: %s (run `kvmx enable --dry-run`)\n", r.Potential)
		}
		if len(entries) > 0 {
			fmt.Fprintf(a.out, "%d kvmx change(s) recorded; undo with `kvmx revert`\n", len(entries))
		}
	}
	return code
}

type check struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // ok | warn | fail | info
	Summary string `json:"summary"`
	Hint    string `json:"hint,omitempty"`
}

func doctorChecks(r *detect.Report) []check {
	var cs []check
	add := func(id, status, summary, hint string) { cs = append(cs, check{id, status, summary, hint}) }

	switch {
	case r.CPU.VirtExt == "vmx" || r.CPU.VirtExt == "svm":
		add("cpu-virt", "ok", "CPU exposes "+strings.ToUpper(r.CPU.VirtExt), "")
	case r.CPU.Arch == "aarch64":
		add("cpu-virt", "info", "arm64: hardware virtualization is only provable by /dev/kvm", "")
	default:
		add("cpu-virt", "fail", "no vmx/svm CPU flag", "see manual steps below")
	}
	if r.CPU.Arch != "x86_64" || r.CPU.VirtExt != "none" { // without vmx/svm the module and node checks only add noise
		switch {
		case r.KVM.ModuleLoaded:
			add("kvm-module", "ok", r.KVM.Module+" is loaded", "")
		case r.KVM.ModuleAvailable:
			add("kvm-module", "fail", r.KVM.Module+" available but not loaded", "kvmx enable")
		default:
			add("kvm-module", "fail", "no KVM module in the running kernel", "install a kernel with KVM, or fix it on the host for containers")
		}
		switch {
		case r.KVM.Usable:
			add("dev-kvm", "ok", "/dev/kvm works (API 12, VM creation OK)", "")
		case r.KVM.DevExists:
			add("dev-kvm", "fail", "/dev/kvm exists but is not usable: "+r.KVM.Probe.OpenErr+r.KVM.Probe.APIErr+r.KVM.Probe.CreateErr, "kvmx enable")
		default:
			add("dev-kvm", "fail", "/dev/kvm is missing", "kvmx enable, or `kvmx flags docker` inside containers")
		}
	}
	switch r.KVM.NestedParam {
	case "Y":
		add("nested", "ok", "nested virtualization enabled in "+r.KVM.Module, "")
	case "N":
		add("nested", "warn", "nested=N: VMs hosted here cannot run KVM", "kvmx enable")
	}
	if r.Tools.QEMU != "" {
		add("qemu", "ok", fmt.Sprintf("QEMU %s at %s (tcg:%v)", r.Tools.QEMUVersion, r.Tools.QEMU, r.Tools.TCG), "")
	} else {
		add("qemu", "fail", "QEMU for "+r.CPU.Arch+" is not installed", "kvmx enable")
	}
	switch {
	case r.Sandbox.UID == 0 || r.Sandbox.SudoPasswdless:
		add("privilege", "ok", "can change the system (root or passwordless sudo)", "")
	case r.Sandbox.HasSudo:
		add("privilege", "info", "sudo needs a password; kvmx enable will prompt", "")
	default:
		add("privilege", "warn", "no root and no sudo: system-level fixes are not possible", "ask the administrator, or use TCG")
	}
	if r.Virt.IsContainer {
		add("container", "info", fmt.Sprintf("%s container: cap_mknod=%v cap_sys_module=%v sysfs_ro=%v seccomp=%d",
			r.Virt.ContainerType, r.Sandbox.CapMknod, r.Sandbox.CapSysModule, r.Sandbox.SysfsReadOnly, r.Sandbox.Seccomp), "")
	}
	if r.Sandbox.ModulesLocked {
		add("modules-locked", "warn", "kernel.modules_disabled=1: modules cannot be loaded until reboot", "")
	}
	opt := map[string]string{"libvirt (virsh)": r.Tools.Libvirt, "virtiofsd": r.Tools.Virtiofsd, "user-mode Linux": r.Tools.UML}
	for _, name := range []string{"libvirt (virsh)", "virtiofsd", "user-mode Linux"} {
		if opt[name] != "" {
			add("tool", "info", name+": "+opt[name], "")
		}
	}
	if r.Tools.ShimActive {
		add("shims", "ok", "kvmx compatibility shims are installed", "")
	}
	return cs
}

func (a *app) cmdDoctor(args []string) int {
	var c common
	fs := a.flagSet("doctor", &c)
	if code, ok := parse(fs, args); !ok {
		return code
	}
	_, r, err := a.collect(&c, true)
	if err != nil {
		return a.fail("%v", err)
	}
	cs := doctorChecks(r)
	manual := guide.Manual(r)
	if c.json {
		a.emit(map[string]any{"report": r, "checks": cs, "manual_steps": manual})
		return exitOK
	}
	mark := map[string]string{"ok": a.paint("32", "ok  "), "warn": a.paint("33", "warn"), "fail": a.paint("31", "FAIL"), "info": "info"}
	fmt.Fprintf(a.out, "kvmx doctor - %s\n\n", a.modeStyled(r.Mode))
	for _, ch := range cs {
		fmt.Fprintf(a.out, "  [%s] %s\n", mark[ch.Status], ch.Summary)
		if ch.Hint != "" {
			fmt.Fprintf(a.out, "         -> %s\n", ch.Hint)
		}
	}
	if len(manual) > 0 {
		fmt.Fprintln(a.out, "\nThings only the owner of the layer below can change:")
		for _, m := range manual {
			fmt.Fprintf(a.out, "\n  * %s\n", m.Title)
			for _, s := range m.Steps {
				fmt.Fprintf(a.out, "      %s\n", s)
			}
			if m.URL != "" {
				fmt.Fprintf(a.out, "      docs: %s\n", m.URL)
			}
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(a.out, "\nnote: %s\n", n)
	}
	fmt.Fprintf(a.out, "\nNext: `kvmx enable --dry-run` shows what kvmx can fix; `kvmx run --demo` boots a test guest in %s.\n", r.Mode)
	return exitOK
}
