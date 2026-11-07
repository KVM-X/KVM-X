package qemu

import (
	"fmt"
	"regexp"
	"strings"
)

// RewriteForTCG converts a QEMU command line written for KVM into one that
// runs under TCG: -enable-kvm and accel=kvm become tcg, `-cpu host` becomes
// `-cpu max`, and options that only exist for KVM are dropped. It returns the
// new args and a human-readable list of what changed. Command lines that do
// not mention KVM at all only gain an explicit multi-threaded TCG accelerator.
func RewriteForTCG(args []string, p Profile) (out []string, changes []string) {
	if p.Name == "" {
		p = Profiles[DefaultProfile]
	}
	note := func(f string, a ...any) { changes = append(changes, fmt.Sprintf(f, a...)) }
	haveAccel := false

	for i := 0; i < len(args); i++ {
		a := args[i]
		name := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-") // accept -opt and --opt
		next := func() (string, bool) {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		}
		switch {
		case !strings.HasPrefix(a, "-"):
			out = append(out, a)
		case name == "enable-kvm":
			note("dropped -enable-kvm")
		case name == "accel" || name == "machine" || name == "M" || name == "cpu" || name == "global":
			v, ok := next()
			if !ok {
				out = append(out, a)
				continue
			}
			i++
			switch name {
			case "accel":
				nv, changed := rewriteAccel(v, p)
				if strings.HasPrefix(nv, "tcg") {
					haveAccel = true
				}
				if changed {
					note("-accel %s -> %s", v, nv)
				}
				out = append(out, a, nv)
			case "machine", "M":
				nv, acc := rewriteMachine(v)
				if acc {
					haveAccel = true
				}
				if nv != v {
					note("-machine %s -> %s", v, nv)
				}
				out = append(out, a, nv)
			case "cpu":
				nv := rewriteCPU(v)
				if nv != v {
					note("-cpu %s -> %s", v, nv)
				}
				out = append(out, a, nv)
			case "global":
				if strings.HasPrefix(v, "kvm-") || strings.HasPrefix(v, "kvmclock") {
					note("dropped -global %s (KVM only)", v)
					continue
				}
				out = append(out, a, v)
			}
		default:
			out = append(out, a)
		}
	}
	if !haveAccel {
		out = append([]string{"-accel", tcgAccel(p)}, out...)
		note("added -accel %s", tcgAccel(p))
	}
	return out, changes
}

func tcgAccel(p Profile) string {
	s := "tcg,thread=" + orDefault(p.Thread, "multi")
	if p.TBSizeMB > 0 {
		s += fmt.Sprintf(",tb-size=%d", p.TBSizeMB)
	}
	return s
}

// rewriteAccel handles "kvm", "kvm:tcg", "kvm,kernel-irqchip=on", "tcg,thread=single".
func rewriteAccel(v string, p Profile) (string, bool) {
	head, _, _ := strings.Cut(v, ",") // kvm-only options (kernel-irqchip, ...) are dropped
	if !strings.Contains(head, "kvm") && !strings.Contains(head, "xen") {
		return v, false // already tcg (or hvf etc.): respect the user's options
	}
	return tcgAccel(p), true
}

var kvmOnlyMachineOpt = regexp.MustCompile(`^(kernel[_-]irqchip|kvm[_-]shadow[_-]mem|xen[_-].*|vmport)=`)

// rewriteMachine returns the new value and whether it pins accel=tcg.
func rewriteMachine(v string) (string, bool) {
	parts := strings.Split(v, ",")
	var keep []string
	pinned := false
	for _, pt := range parts {
		switch {
		case strings.HasPrefix(pt, "accel="):
			keep = append(keep, "accel=tcg")
			pinned = true
		case kvmOnlyMachineOpt.MatchString(pt):
			// dropped
		default:
			keep = append(keep, pt)
		}
	}
	return strings.Join(keep, ","), pinned
}

var kvmOnlyCPUFeature = regexp.MustCompile(`^([+-]?)(kvm|hv)[-_=a-z0-9]*$|^migratable=|^([+-]?)(vmx|svm)$|^(vmx|svm)=`)

func rewriteCPU(v string) string {
	parts := strings.Split(v, ",")
	model := parts[0]
	if model == "host" || model == "max" {
		model = "max"
	}
	keep := []string{model}
	for _, f := range parts[1:] {
		if kvmOnlyCPUFeature.MatchString(f) {
			continue
		}
		keep = append(keep, f)
	}
	return strings.Join(keep, ",")
}

// ---- libvirt ----

var (
	reDomainKVM  = regexp.MustCompile(`(<domain\b[^>]*\btype\s*=\s*)(['"])kvm(['"])`)
	reCPUBlock   = regexp.MustCompile(`(?s)<cpu\b[^>]*?(/>|>.*?</cpu>)`)
	reHostCPU    = regexp.MustCompile(`mode\s*=\s*['"]host-(passthrough|model)['"]`)
	reHyperv     = regexp.MustCompile(`(?s)\s*<hyperv\b[^>]*?(/>|>.*?</hyperv>)`)
	reKVMFeature = regexp.MustCompile(`(?s)\s*<kvm\b[^>]*?(/>|>.*?</kvm>)`)
	reKVMTimer   = regexp.MustCompile(`\s*<timer\b[^>]*name\s*=\s*['"](kvmclock|hypervclock)['"][^>]*/>`)
	reIOAPIC     = regexp.MustCompile(`(<ioapic\b[^>]*driver\s*=\s*)(['"])kvm(['"])`)
)

// RewriteDomainXML turns a libvirt domain written for KVM into one libvirt can
// start under TCG (type='qemu').
func RewriteDomainXML(x string) (string, []string) {
	var changes []string
	sub := func(re *regexp.Regexp, repl, msg string) {
		if re.MatchString(x) {
			x = re.ReplaceAllString(x, repl)
			changes = append(changes, msg)
		}
	}
	sub(reDomainKVM, `${1}${2}qemu${3}`, "domain type kvm -> qemu")
	if m := reCPUBlock.FindString(x); m != "" && reHostCPU.MatchString(m) {
		x = strings.Replace(x, m, `<cpu mode='custom' match='exact' check='none'><model fallback='allow'>max</model></cpu>`, 1)
		changes = append(changes, "host-passthrough/host-model CPU -> custom max")
	}
	sub(reHyperv, "", "removed <hyperv> enlightenments")
	sub(reKVMFeature, "", "removed <kvm> features")
	sub(reKVMTimer, "", "removed kvmclock/hypervclock timers")
	sub(reIOAPIC, `${1}${2}qemu${3}`, "ioapic driver kvm -> qemu")
	return x, changes
}

// DomainOpts describes a minimal VM for GenerateDomainXML.
type DomainOpts struct {
	Name      string
	Arch      string
	MemMB     int
	CPUs      int
	Disk      string
	Format    string
	Mode      string // kvm | tcg
	TBCacheMB int    // libvirt >= 8.0 only; 0 omits
	Kernel    string
	Initrd    string
	Cmdline   string
}

// GenerateDomainXML emits a libvirt domain. In tcg mode it is type='qemu'
// with the max CPU model; in kvm mode type='kvm' with host-passthrough.
func GenerateDomainXML(o DomainOpts) string {
	if o.Arch == "" {
		o.Arch = "x86_64"
	}
	machine := "q35"
	if o.Arch == "aarch64" {
		machine = "virt"
	}
	typ, cpu, features := "qemu", `<cpu mode='custom' match='exact' check='none'><model fallback='allow'>max</model></cpu>`, "<acpi/><apic/>"
	if o.Mode == "kvm" {
		typ, cpu = "kvm", `<cpu mode='host-passthrough' check='none'/>`
	} else if o.TBCacheMB > 0 {
		features += fmt.Sprintf("<tcg><tb-cache unit='MiB'>%d</tb-cache></tcg>", o.TBCacheMB)
	}
	if o.Arch == "aarch64" {
		features = "<gic version='3'/>"
	}
	if o.Format == "" {
		o.Format = ImageFormat(o.Disk)
	}
	osBlock := fmt.Sprintf("<type arch='%s' machine='%s'>hvm</type>", o.Arch, machine)
	if o.Kernel != "" {
		osBlock += fmt.Sprintf("<kernel>%s</kernel>", o.Kernel)
		if o.Initrd != "" {
			osBlock += fmt.Sprintf("<initrd>%s</initrd>", o.Initrd)
		}
		if o.Cmdline != "" {
			osBlock += fmt.Sprintf("<cmdline>%s</cmdline>", o.Cmdline)
		}
	} else {
		osBlock += "<boot dev='hd'/>"
	}
	disk := ""
	if o.Disk != "" {
		disk = fmt.Sprintf("<disk type='file' device='disk'><driver name='qemu' type='%s'/><source file='%s'/><target dev='vda' bus='virtio'/></disk>", o.Format, o.Disk)
	}
	return fmt.Sprintf(`<domain type='%s'>
  <name>%s</name>
  <memory unit='MiB'>%d</memory>
  <vcpu>%d</vcpu>
  <os>%s</os>
  <features>%s</features>
  %s
  <devices>
    %s
    <interface type='user'><model type='virtio'/></interface>
    <console type='pty'/>
    <rng model='virtio'><backend model='random'>/dev/urandom</backend></rng>
  </devices>
</domain>
`, typ, o.Name, o.MemMB, max(o.CPUs, 1), osBlock, features, cpu, disk)
}
