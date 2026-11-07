package detect

import "strings"

// Blocker IDs. Plan steps in internal/plan key off these.
const (
	BCPUNoVirt       = "cpu-no-virt-ext"
	BKernelNoKVM     = "kvm-kernel-missing"
	BModuleNotLoaded = "kvm-module-not-loaded"
	BDevMissing      = "dev-kvm-missing"
	BContainerNoDev  = "container-no-kvm-device"
	BDevPermission   = "dev-kvm-permission"
	BContainerDenied = "container-kvm-denied"
	BKVMBroken       = "kvm-unusable"
	BQEMUMissing     = "qemu-missing"
	BNestedOff       = "nested-param-off" // advisory: guests of this host cannot run KVM
)

// Blocker returns the blocker with the given ID.
func (r *Report) Blocker(id string) (Blocker, bool) {
	for _, b := range r.Blockers {
		if b.ID == id {
			return b, true
		}
	}
	return Blocker{}, false
}

func (r *Report) HasBlocker(id string) bool {
	_, ok := r.Blocker(id)
	return ok
}

func classify(r *Report) {
	guest := r.CPU.Hypervisor || r.Virt.IsVM
	switch {
	case r.KVM.Usable && guest:
		r.Mode = ModeNested
	case r.KVM.Usable:
		r.Mode = ModeHardware
	default:
		r.Mode = ModeTCG
	}
	r.ModeCode, r.Penalty = r.Mode.Code(), r.Mode.Penalty()
	r.Blockers = []Blocker{}

	if !r.KVM.Usable {
		kvmBlockers(r, guest)
	}
	if r.KVM.NestedParam == "N" && r.KVM.ModuleLoaded {
		r.Blockers = append(r.Blockers, Blocker{ID: BNestedOff, Fixable: r.KVM.ModuleAvailable,
			Summary: r.KVM.Module + " nested=N",
			Detail:  "Virtual machines hosted on this machine cannot run KVM themselves until nested=1 is set."})
	}
	if r.Tools.QEMU == "" {
		r.Blockers = append(r.Blockers, Blocker{ID: BQEMUMissing, Fixable: r.Tools.PkgManager != "",
			Summary: "QEMU for " + r.CPU.Arch + " is not installed",
			Detail:  "Needed for both KVM and the TCG software fallback."})
	}

	switch {
	case r.Mode != ModeTCG:
		r.Ready = r.Tools.QEMU != ""
		if !r.Ready {
			r.ReadyNote = "KVM works but QEMU is not installed; run `kvmx enable`"
		}
	case r.Tools.QEMU == "":
		r.ReadyNote = "QEMU is not installed; run `kvmx enable` to install it"
	case !r.Tools.TCG:
		r.ReadyNote = "installed QEMU was built without TCG"
	default:
		r.Ready = true
	}

	r.Potential = ModeTCG
	if r.Mode != ModeTCG {
		r.Potential = r.Mode
	} else if kvmFixable(r) {
		r.Potential = ModeHardware
		if guest {
			r.Potential = ModeNested
		}
	}
	r.Notes = notes(r)
}

// kvmFixable: every KVM blocker has a kvmx fix.
func kvmFixable(r *Report) bool {
	n := 0
	for _, b := range r.Blockers {
		switch b.ID {
		case BQEMUMissing, BNestedOff:
			continue
		}
		n++
		if !b.Fixable {
			return false
		}
	}
	return n > 0
}

func kvmBlockers(r *Report, guest bool) {
	add := func(b Blocker) { r.Blockers = append(r.Blockers, b) }
	k, c := r.KVM, r.CPU
	x86 := c.Arch == "x86_64"

	if x86 && c.VirtExt == "none" {
		d := "The CPU does not advertise VT-x/AMD-V (no vmx/svm flag). Check firmware settings (Intel VT-x / AMD SVM Mode) and CPU support."
		if guest {
			d = "The hypervisor does not expose VT-x/AMD-V to this guest (no vmx/svm flag): nested virtualization is off for this instance at the provider/host level."
		}
		add(Blocker{ID: BCPUNoVirt, Summary: "no hardware virtualization extension visible", Detail: d})
		return
	}
	if c.Arch != "x86_64" && c.Arch != "aarch64" {
		add(Blocker{ID: BKernelNoKVM, Summary: "KVM is not supported by kvmx on " + c.Arch})
		return
	}

	if !k.ModuleLoaded {
		switch {
		case !k.ModuleAvailable:
			d := "The running kernel ships no " + k.Module + " module and is not built with it."
			if c.Arch == "aarch64" {
				d = "No KVM in this kernel: arm64 KVM needs the kernel to boot at EL2 (hypervisor mode), which most cloud VMs and many boards do not provide."
			}
			if r.Virt.IsContainer {
				d += " Containers share the host kernel: fix it on the host."
			}
			add(Blocker{ID: BKernelNoKVM, Summary: "kernel has no usable KVM", Detail: d})
		case r.Virt.IsContainer && (!r.Sandbox.CapSysModule || r.Sandbox.UID != 0):
			add(Blocker{ID: BModuleNotLoaded, Summary: k.Module + " is not loaded and this container cannot load modules",
				Detail: "Run `sudo modprobe " + k.Module + "` on the host."})
		case r.Sandbox.ModulesLocked:
			add(Blocker{ID: BModuleNotLoaded, Summary: k.Module + " is not loaded and module loading is locked (kernel.modules_disabled=1)",
				Detail: "Reboot with the module in the initramfs, or unlock via your boot configuration."})
		default:
			add(Blocker{ID: BModuleNotLoaded, Fixable: true, Summary: k.Module + " is not loaded"})
		}
		return
	}

	switch {
	case !k.DevExists:
		canMknod := r.Sandbox.UID == 0 || r.Sandbox.HasSudo
		if r.Virt.IsContainer {
			canMknod = r.Sandbox.CapMknod
			add(Blocker{ID: BContainerNoDev, Fixable: canMknod,
				Summary: "KVM is loaded on the host but /dev/kvm is not passed into this container",
				Detail:  "Start the container with the KVM device (see `kvmx flags docker`)."})
			return
		}
		add(Blocker{ID: BDevMissing, Fixable: canMknod, Summary: k.Module + " is loaded but /dev/kvm is missing"})
	case !k.Probe.Opened && (strings.Contains(k.Probe.OpenErr, "permission denied") || strings.Contains(k.Probe.OpenErr, "not permitted")):
		if r.Virt.IsContainer && k.InKVMGroupProc {
			add(Blocker{ID: BContainerDenied, Summary: "/dev/kvm exists but the container's device cgroup denies access",
				Detail: "Allow the device on the host (see `kvmx flags docker`)."})
			return
		}
		add(Blocker{ID: BDevPermission, Fixable: r.Sandbox.UID == 0 || r.Sandbox.HasSudo,
			Summary: "/dev/kvm exists but " + r.Sandbox.User + " cannot open it (" + k.DevMode + " " + k.DevGroup + ")"})
	case !k.Probe.Opened:
		add(Blocker{ID: BKVMBroken, Summary: "cannot open /dev/kvm: " + k.Probe.OpenErr,
			Detail: "ENODEV usually means virtualization is disabled in firmware or masked by the hypervisor."})
	default:
		d := k.Probe.APIErr + k.Probe.CreateErr
		add(Blocker{ID: BKVMBroken, Summary: "/dev/kvm opens but is not functional", Detail: d})
	}
}

func notes(r *Report) []string {
	var n []string
	if r.CPU.GenericVM && r.CPU.VirtExt == "none" && r.Provider.ID == "qemu" {
		n = append(n, "The guest CPU model is generic ("+r.CPU.Model+"): on Proxmox/libvirt set the VM CPU type to `host` so vmx/svm is passed through.")
	}
	if r.Tools.ShimActive {
		n = append(n, "kvmx shims are installed ahead of the real QEMU on PATH.")
	}
	if r.Tools.UML != "" && !r.KVM.Usable {
		n = append(n, "user-mode Linux is installed: `kvmx run --backend uml` runs Linux guests at near-native CPU speed without KVM (not a VM; syscalls are slower).")
	}
	if r.KVM.MSHV {
		n = append(n, "/dev/mshv is present (Microsoft hypervisor root partition); QEMU's mshv accelerator may be usable.")
	}
	if r.Virt.IsContainer && r.Mode == ModeTCG && r.CPU.VirtExt != "none" && r.KVM.ModuleLoaded {
		n = append(n, "The host supports KVM; this container simply lacks the device.")
	}
	return n
}
