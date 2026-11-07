// Package qemu builds QEMU command lines for the KVM and TCG rungs of the
// ladder, and rewrites foreign command lines so KVM-only options degrade to
// software emulation instead of failing.
package qemu

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KVM-X/KVM-X/internal/sys"
)

// Profile is a named set of TCG tuning options. The numbers come from
// `kvmx bench`; see docs/BENCHMARKS.md for how they were chosen.
type Profile struct {
	Name     string
	Thread   string // multi | single
	TBSizeMB int    // translation-block cache; 0 = QEMU default
	CPU      string // x86 CPU model under TCG
	Machine  string // x86 machine type
	// Cmdline is appended to the kernel command line for direct-kernel boots.
	Cmdline string
	// MaxCPUs caps -smp: MTTCG stops scaling long before core count.
	MaxCPUs int
}

var Profiles = map[string]Profile{
	// compat: fastest per vCPU and the oldest ISA (qemu64: no SSE4.2/AVX), so some modern distros will not boot.
	"compat": {Name: "compat", Thread: "single", CPU: "qemu64", Machine: "pc", MaxCPUs: 1},
	// balanced: modern ISA (-cpu max), multi-threaded TCG so SMP guests scale.
	"balanced": {Name: "balanced", Thread: "multi", TBSizeMB: 256, CPU: "max", Machine: "q35", MaxCPUs: 4},
}

// DefaultProfile is used when the user does not pick one.
const DefaultProfile = "balanced"

type Share struct {
	Dir, Tag string
	Mode     string // 9p | virtiofs
	Socket   string // virtiofs only: unix socket of the running virtiofsd
}

type Spec struct {
	Arch    string // x86_64 | aarch64
	Mode    string // kvm | tcg
	Profile Profile
	CPUs    int
	MemMB   int
	Machine string // override
	Disk    string
	Format  string // raw | qcow2 (auto from extension when empty)
	CDROM   string
	Kernel  string
	Initrd  string
	Append  string
	NoNet   bool
	HostFwd []string // e.g. tcp::2222-:22
	Shares  []Share
	Display string // none | vnc=:0 | gtk | sdl
	// Firmware is a UEFI image, needed to boot disks on aarch64.
	Firmware string
	Extra    []string
}

// Binary returns the qemu-system binary for arch.
func Binary(arch string) string { return "qemu-system-" + arch }

// Find locates the real (non-shim) emulator for arch.
func Find(s sys.System, arch string) (string, error) {
	for _, n := range []string{Binary(arch), "qemu-kvm"} {
		for _, p := range s.LookPathAll(n) {
			if !sys.IsShim(p) {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("%s not found; run `kvmx enable` to install QEMU", Binary(arch))
}

// FindFirmware looks for an aarch64 UEFI image in the usual distro locations.
func FindFirmware(arch string) string {
	if arch != "aarch64" {
		return ""
	}
	for _, p := range []string{
		"/usr/share/AAVMF/AAVMF_CODE.fd", "/usr/share/qemu-efi-aarch64/QEMU_EFI.fd",
		"/usr/share/edk2/aarch64/QEMU_EFI.fd", "/usr/share/edk2/aarch64/QEMU_CODE.fd",
		"/usr/share/qemu/edk2-aarch64-code.fd", "/usr/share/edk2-ovmf/aarch64/QEMU_EFI.fd",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Validate checks the spec is bootable.
func (sp *Spec) Validate() error {
	if sp.Arch != "x86_64" && sp.Arch != "aarch64" {
		return fmt.Errorf("unsupported guest architecture %q", sp.Arch)
	}
	if sp.Mode != "kvm" && sp.Mode != "tcg" {
		return fmt.Errorf("mode must be kvm or tcg, got %q", sp.Mode)
	}
	if sp.Disk == "" && sp.CDROM == "" && sp.Kernel == "" {
		return errors.New("nothing to boot: give --disk, --cdrom or --kernel (or try `kvmx run --demo`)")
	}
	if sp.Arch == "aarch64" && sp.Kernel == "" && sp.Firmware == "" {
		return errors.New("booting a disk on aarch64 needs UEFI firmware (install qemu-efi-aarch64 / edk2-aarch64) or use --kernel")
	}
	return nil
}

func (sp *Spec) bus() string {
	if sp.machine() == "microvm" {
		return "-device"
	}
	return "-pci"
}

func (sp *Spec) machine() string {
	if sp.Machine != "" {
		return sp.Machine
	}
	if sp.Arch == "aarch64" {
		return "virt"
	}
	if sp.Profile.Machine != "" {
		return sp.Profile.Machine
	}
	return "q35"
}

// Args renders the QEMU argument vector (without the binary).
func (sp *Spec) Args() ([]string, error) {
	if err := sp.Validate(); err != nil {
		return nil, err
	}
	p := sp.Profile
	if p.Name == "" {
		p = Profiles[DefaultProfile]
	}
	cpus := sp.CPUs
	if cpus <= 0 {
		cpus = 2
	}
	if sp.Mode == "tcg" && p.MaxCPUs > 0 && cpus > p.MaxCPUs && sp.CPUs <= 0 {
		cpus = p.MaxCPUs
	}
	mem := sp.MemMB
	if mem <= 0 {
		mem = 1024
	}
	bus := sp.bus()
	dev := func(name string) string { // virtio-blk + -pci, or virtio-blk-device on microvm
		if bus == "-pci" {
			return name + "-pci"
		}
		return name + "-device"
	}

	machine := sp.machine()
	a := []string{"-nodefaults", "-no-user-config", "-no-reboot"}
	var accel, cpu string
	switch sp.Mode {
	case "kvm":
		accel, cpu = "kvm", "host"
		if sp.Arch == "aarch64" {
			machine += ",gic-version=host"
		}
	default:
		accel = "tcg,thread=" + orDefault(p.Thread, "multi")
		if p.TBSizeMB > 0 {
			accel += fmt.Sprintf(",tb-size=%d", p.TBSizeMB)
		}
		cpu = "max"
		if sp.Arch == "x86_64" && p.CPU != "" {
			cpu = p.CPU
		}
	}
	a = append(a, "-machine", machine, "-accel", accel, "-cpu", cpu, "-smp", fmt.Sprint(cpus), "-m", fmt.Sprintf("%dM", mem))

	gpu := "virtio-vga"
	if sp.Arch == "aarch64" {
		gpu = "virtio-gpu-pci"
	}
	hasVirtiofs := false
	for _, s := range sp.Shares {
		if s.Mode == "virtiofs" {
			hasVirtiofs = true
		}
	}
	if hasVirtiofs {
		a = append(a, "-object", fmt.Sprintf("memory-backend-memfd,id=mem,size=%dM,share=on", mem), "-numa", "node,memdev=mem")
	}

	switch {
	case sp.Display == "" || sp.Display == "none":
		a = append(a, "-display", "none", "-serial", "mon:stdio")
	case strings.HasPrefix(sp.Display, "vnc="):
		a = append(a, "-vnc", strings.TrimPrefix(sp.Display, "vnc="), "-device", gpu, "-serial", "mon:stdio")
	default:
		a = append(a, "-display", sp.Display, "-device", gpu, "-serial", "mon:stdio")
	}

	if sp.Firmware != "" && sp.Kernel == "" {
		a = append(a, "-bios", sp.Firmware)
	}
	if sp.Kernel != "" {
		a = append(a, "-kernel", sp.Kernel)
		if sp.Initrd != "" {
			a = append(a, "-initrd", sp.Initrd)
		}
		cmdline := sp.Append
		if cmdline == "" {
			cmdline = "console=" + map[string]string{"x86_64": "ttyS0", "aarch64": "ttyAMA0"}[sp.Arch]
		}
		if sp.Mode == "tcg" && p.Cmdline != "" {
			cmdline += " " + p.Cmdline
		}
		a = append(a, "-append", cmdline)
	}
	if sp.Disk != "" {
		f := sp.Format
		if f == "" {
			f = ImageFormat(sp.Disk)
		}
		a = append(a, "-drive", fmt.Sprintf("file=%s,if=none,id=d0,format=%s", sp.Disk, f),
			"-device", dev("virtio-blk")+",drive=d0")
	}
	if sp.CDROM != "" {
		if sp.Arch == "x86_64" {
			a = append(a, "-drive", "file="+sp.CDROM+",media=cdrom,if=ide,readonly=on")
		} else {
			a = append(a, "-drive", "file="+sp.CDROM+",if=none,id=cd0,media=cdrom,readonly=on",
				"-device", "virtio-scsi-pci,id=scsi0", "-device", "scsi-cd,drive=cd0,bus=scsi0.0")
		}
	}
	if !sp.NoNet {
		nd := "user,id=n0"
		for _, f := range sp.HostFwd {
			nd += ",hostfwd=" + f
		}
		a = append(a, "-netdev", nd, "-device", dev("virtio-net")+",netdev=n0")
	}
	a = append(a, "-object", "rng-random,id=rng0,filename=/dev/urandom", "-device", dev("virtio-rng")+",rng=rng0")

	for i, s := range sp.Shares {
		switch s.Mode {
		case "virtiofs":
			a = append(a, "-chardev", fmt.Sprintf("socket,id=charfs%d,path=%s", i, s.Socket),
				"-device", fmt.Sprintf("vhost-user-fs-pci,chardev=charfs%d,tag=%s", i, s.Tag))
		default:
			a = append(a, "-virtfs", fmt.Sprintf("local,path=%s,mount_tag=%s,security_model=none,id=fs%d", s.Dir, s.Tag, i))
		}
	}
	return append(a, sp.Extra...), nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ImageFormat guesses raw vs qcow2 from the extension.
func ImageFormat(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".qcow2", ".qcow":
		return "qcow2"
	}
	return "raw"
}
