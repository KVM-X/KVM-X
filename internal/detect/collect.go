package detect

import (
	"bytes"
	"compress/gzip"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/KVM-X/KVM-X/internal/sys"
)

// Options tune what Collect is allowed to touch.
type Options struct {
	Version   string
	Metadata  bool // opt-in: query 169.254.169.254 for the instance type
	ProbeSudo bool // run `sudo -n true`
}

// Collect gathers facts and classifies the machine.
func Collect(s sys.System, o Options) *Report {
	r := &Report{Schema: SchemaVersion, Version: o.Version}
	r.CPU = parseCPU(s)
	r.Virt = detectVirt(s, r.CPU)
	r.Provider = detectProvider(s, r.Virt, r.CPU, o.Metadata)
	r.Distro = parseOSRelease(s)
	r.Sandbox = detectSandbox(s, o.ProbeSudo)
	r.KVM = detectKVM(s, r.CPU)
	r.Tools = detectTools(s, r.CPU.Arch, r.Distro)
	classify(r)
	return r
}

func read(s sys.System, p string) string {
	b, err := s.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---- CPU ----

func archName(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "riscv64":
		return "riscv64"
	case "386":
		return "i386"
	}
	return goarch
}

func parseCPU(s sys.System) CPU {
	c := CPU{Arch: archName(s.GOARCH()), VirtExt: "none"}
	var flagLine string
	for _, line := range strings.Split(read(s, "/proc/cpuinfo"), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			c.Cores++
		case "vendor_id":
			if c.Vendor == "" {
				c.Vendor = v
			}
		case "model name", "Hardware":
			if c.Model == "" {
				c.Model = v
			}
		case "flags", "Features":
			if flagLine == "" {
				flagLine = v
			}
		}
	}
	flags := map[string]bool{}
	for _, f := range strings.Fields(flagLine) {
		flags[f] = true
	}
	for _, f := range []string{"vmx", "svm", "ept", "vpid", "npt", "hypervisor"} {
		if flags[f] {
			c.VirtFlags = append(c.VirtFlags, f)
		}
	}
	c.Hypervisor = flags["hypervisor"]
	switch {
	case flags["vmx"]:
		c.VirtExt = "vmx"
	case flags["svm"]:
		c.VirtExt = "svm"
	case c.Arch != "x86_64":
		c.VirtExt = "n/a"
	}
	m := strings.ToLower(c.Model)
	c.GenericVM = strings.Contains(m, "qemu virtual cpu") || strings.Contains(m, "common kvm processor") ||
		strings.Contains(m, "kvm64") || strings.Contains(m, "qemu64")
	return c
}

// ---- virtualization type ----

func detectVirt(s sys.System, cpu CPU) Virt {
	v := Virt{}
	add := func(e string) { v.Evidence = append(v.Evidence, e) }

	if out, _ := s.Exec("systemd-detect-virt", "--vm"); sys.Trim(out) != "" && sys.Trim(out) != "none" {
		v.IsVM, v.VMType = true, sys.Trim(out)
		add("systemd-detect-virt --vm: " + v.VMType)
	}
	if out, _ := s.Exec("systemd-detect-virt", "--container"); sys.Trim(out) != "" && sys.Trim(out) != "none" {
		v.IsContainer, v.ContainerType = true, sys.Trim(out)
		add("systemd-detect-virt --container: " + v.ContainerType)
	}
	if !v.IsVM && !v.IsContainer {
		if out, err := s.Exec("virt-what"); err == nil {
			for _, l := range strings.Fields(out) {
				switch l {
				case "docker", "lxc", "podman", "openvz", "systemd-nspawn":
					v.IsContainer, v.ContainerType = true, l
				default:
					if !v.IsVM {
						v.IsVM, v.VMType = true, l
					}
				}
				add("virt-what: " + l)
			}
		}
	}
	if !v.IsContainer {
		switch {
		case s.Stat("/.dockerenv").Exists:
			v.IsContainer, v.ContainerType = true, "docker"
			add("/.dockerenv present")
		case s.Stat("/run/.containerenv").Exists:
			v.IsContainer, v.ContainerType = true, "podman"
			add("/run/.containerenv present")
		case s.Stat("/proc/vz").Exists && !s.Stat("/proc/bc").Exists:
			v.IsContainer, v.ContainerType = true, "openvz"
			add("/proc/vz present without /proc/bc (OpenVZ container)")
		case strings.Contains(read(s, "/proc/1/environ"), "container=lxc"):
			v.IsContainer, v.ContainerType = true, "lxc"
			add("PID 1 environment container=lxc")
		}
	}
	if !v.IsContainer {
		cg := read(s, "/proc/1/cgroup")
		for _, kw := range [][2]string{{"/docker/", "docker"}, {"/lxc/", "lxc"}, {"kubepods", "kubernetes"}, {"libpod", "podman"}} {
			if strings.Contains(cg, kw[0]) {
				v.IsContainer, v.ContainerType = true, kw[1]
				add("PID 1 cgroup mentions " + kw[0])
				break
			}
		}
	}
	if strings.Contains(strings.ToLower(read(s, "/proc/sys/kernel/osrelease")), "microsoft") {
		v.IsContainer, v.ContainerType = true, "wsl"
		add("kernel release mentions microsoft (WSL)")
	}
	if !v.IsVM && s.Stat("/proc/xen").Exists {
		v.IsVM, v.VMType = true, "xen"
		add("/proc/xen present")
	}
	if cpu.Hypervisor {
		add("cpuinfo hypervisor flag set")
		if !v.IsVM {
			v.IsVM, v.VMType = true, "unknown"
		}
	}
	return v
}

// ---- provider (offline, from DMI) ----

func dmi(s sys.System, name string) string {
	return read(s, "/sys/class/dmi/id/"+name)
}

func has(s string, subs ...string) bool {
	s = strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(s, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

func detectProvider(s sys.System, v Virt, cpu CPU, metadata bool) Provider {
	sv, pn, bv, at, pv := dmi(s, "sys_vendor"), dmi(s, "product_name"), dmi(s, "bios_vendor"),
		dmi(s, "chassis_asset_tag"), dmi(s, "product_version")
	p := Provider{Product: pn, Vendor: sv}
	set := func(id, name, ev string) Provider {
		p.ID, p.Name = id, name
		p.Evidence = append(p.Evidence, ev)
		return p
	}
	switch {
	case at == "OracleCloud.com":
		set("oracle", "Oracle Cloud Infrastructure", "chassis_asset_tag=OracleCloud.com")
	case at == "7783-7084-3265-9085-8269-3286-77":
		set("azure", "Microsoft Azure", "chassis_asset_tag is the Azure fixed tag")
	case has(sv, "amazon") || has(bv, "amazon") || has(pv, "amazon"):
		set("aws", "Amazon EC2", "DMI vendor mentions Amazon")
	case has(pn, "google compute engine") || has(bv, "google"):
		set("gcp", "Google Compute Engine", "DMI product/bios is Google")
	case has(sv, "hetzner"):
		set("hetzner", "Hetzner Cloud", "sys_vendor="+sv)
	case has(sv, "digitalocean") || has(pn, "droplet"):
		set("digitalocean", "DigitalOcean", "DMI mentions DigitalOcean/Droplet")
	case has(sv, "vultr"):
		set("vultr", "Vultr", "sys_vendor="+sv)
	case has(sv, "linode") || has(pn, "linode"):
		set("linode", "Linode/Akamai", "DMI mentions Linode")
	case has(sv, "ovh"):
		set("ovh", "OVHcloud", "sys_vendor="+sv)
	case has(sv, "scaleway"):
		set("scaleway", "Scaleway", "sys_vendor="+sv)
	case has(sv, "alibaba"):
		set("alibaba", "Alibaba Cloud", "sys_vendor="+sv)
	case has(pn, "openstack"):
		set("openstack", "OpenStack", "product_name="+pn)
	case has(sv, "vmware"):
		set("vmware", "VMware", "sys_vendor="+sv)
	case has(sv, "innotek") || has(pn, "virtualbox"):
		set("virtualbox", "VirtualBox", "DMI mentions VirtualBox")
	case has(sv, "microsoft") && has(pn, "virtual machine"):
		set("hyperv", "Microsoft Hyper-V", "DMI is Microsoft Virtual Machine")
	case has(sv, "xen") || v.VMType == "xen":
		set("xen", "Xen", "Xen vendor or /proc/xen")
	case has(sv, "qemu") || has(pn, "standard pc") || has(pn, "kvm") || v.VMType == "kvm" || v.VMType == "qemu":
		set("qemu", "QEMU/KVM (Proxmox, libvirt, OpenStack, oVirt, ...)", "DMI or systemd-detect-virt says QEMU/KVM")
	case v.IsVM || cpu.Hypervisor:
		set("unknown-vm", "Unknown hypervisor", "hypervisor flag without a recognised DMI vendor")
	default:
		set("bare-metal", "Bare metal (or a container on bare metal)", "no hypervisor flag, no VM detected")
	}
	if metadata {
		p.InstanceType = instanceType(s, p.ID)
	}
	return p
}

func instanceType(s sys.System, id string) string {
	const base = "http://" + sys.MetadataHost
	switch id {
	case "aws":
		tok, err := s.HTTPGet("PUT", base+"/latest/api/token", map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "60"})
		if err != nil {
			return ""
		}
		out, err := s.HTTPGet("GET", base+"/latest/meta-data/instance-type", map[string]string{"X-aws-ec2-metadata-token": sys.Trim(tok)})
		if err == nil {
			return sys.Trim(out)
		}
	case "gcp":
		out, err := s.HTTPGet("GET", base+"/computeMetadata/v1/instance/machine-type", map[string]string{"Metadata-Flavor": "Google"})
		if err == nil {
			return filepath.Base(sys.Trim(out))
		}
	case "azure":
		out, err := s.HTTPGet("GET", base+"/metadata/instance/compute/vmSize?api-version=2021-02-01&format=text", map[string]string{"Metadata": "true"})
		if err == nil {
			return sys.Trim(out)
		}
	case "oracle":
		out, err := s.HTTPGet("GET", base+"/opc/v2/instance/shape", map[string]string{"Authorization": "Bearer Oracle"})
		if err == nil {
			return sys.Trim(out)
		}
	}
	return ""
}

// ---- distro ----

func parseOSRelease(s sys.System) Distro {
	d := Distro{}
	b := read(s, "/etc/os-release")
	if b == "" {
		b = read(s, "/usr/lib/os-release")
	}
	for _, line := range strings.Split(b, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			d.ID = v
		case "VERSION_ID":
			d.Version = v
		case "ID_LIKE":
			d.Like = v
		case "PRETTY_NAME":
			d.Pretty = v
		}
	}
	return d
}

// ---- sandbox: capabilities, seccomp, sysfs, sudo ----

const (
	capSysModule = 16
	capSysAdmin  = 21
	capMknod     = 27
)

func detectSandbox(s sys.System, probeSudo bool) Sandbox {
	sb := Sandbox{UID: s.UID(), User: s.TargetUser()}
	for _, line := range strings.Split(read(s, "/proc/self/status"), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "CapEff":
			if n, err := strconv.ParseUint(v, 16, 64); err == nil {
				sb.CapSysModule = n&(1<<capSysModule) != 0
				sb.CapSysAdmin = n&(1<<capSysAdmin) != 0
				sb.CapMknod = n&(1<<capMknod) != 0
			}
		case "Seccomp":
			sb.Seccomp, _ = strconv.Atoi(v)
		case "NoNewPrivs":
			sb.NoNewPrivs = v == "1"
		}
	}
	for _, line := range strings.Split(read(s, "/proc/self/mountinfo"), "\n") {
		f := strings.Fields(line)
		if len(f) > 5 && f[4] == "/sys" {
			sb.SysfsReadOnly = strings.Contains(","+f[5]+",", ",ro,")
		}
	}
	sb.ModulesLocked = read(s, "/proc/sys/kernel/modules_disabled") == "1"
	rel := read(s, "/proc/sys/kernel/osrelease")
	sb.LibModules = s.Stat("/lib/modules/"+rel).Exists || s.Stat("/usr/lib/modules/"+rel).Exists
	switch {
	case s.Stat("/sys/fs/cgroup/cgroup.controllers").Exists:
		sb.CgroupVersion = 2
	case s.Stat("/sys/fs/cgroup").Exists:
		sb.CgroupVersion = 1
	}
	sb.HasSudo = len(s.LookPathAll("sudo")) > 0
	switch {
	case sb.UID == 0:
		sb.SudoPasswdless = true
	case sb.HasSudo && probeSudo:
		_, err := s.Exec("sudo", "-n", "true")
		sb.SudoPasswdless = err == nil
	}
	return sb
}

// ---- KVM ----

func detectKVM(s sys.System, cpu CPU) KVM {
	k := KVM{MiscMinor: -1}
	switch cpu.VirtExt {
	case "vmx":
		k.Module = "kvm_intel"
	case "svm":
		k.Module = "kvm_amd"
	default:
		k.Module = "kvm"
	}
	rel := read(s, "/proc/sys/kernel/osrelease")
	k.ModuleLoaded = s.Stat("/sys/module/" + k.Module).Exists
	if k.Module != "kvm" {
		k.NestedParam = normBool(read(s, "/sys/module/"+k.Module+"/parameters/nested"))
		k.ModuleRefs, _ = strconv.Atoi(read(s, "/sys/module/"+k.Module+"/refcnt"))
	}
	k.ModuleAvailable, k.ModuleBuiltin = moduleAvailable(s, rel, k.Module)
	if k.ModuleLoaded {
		k.ModuleAvailable = true
	}
	k.KernelConfig = kernelConfig(s, rel)

	for _, line := range strings.Split(read(s, "/proc/misc"), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "kvm" {
			k.MiscMinor, _ = strconv.Atoi(f[0])
		}
	}

	dev := s.Stat("/dev/kvm")
	k.DevExists, k.DevIsChar = dev.Exists, dev.IsChar
	if dev.Exists {
		k.DevMode = "0" + strconv.FormatUint(uint64(dev.Mode.Perm()), 8)
		k.DevOwnerGID, k.DevGroup = dev.GID, dev.Group
		k.Probe = s.ProbeKVM("/dev/kvm")
	}
	k.Usable = k.Probe.Opened && k.Probe.APIVersion == 12 && k.Probe.CreateVMOK
	grp := k.DevGroup
	if grp == "" {
		grp = "kvm"
	}
	for _, g := range s.MemberGroups() {
		if g == grp {
			k.InKVMGroupDB = true
		}
	}
	for _, g := range s.ProcessGIDs() {
		if uint32(g) == k.DevOwnerGID && dev.Exists {
			k.InKVMGroupProc = true
		}
	}
	k.MSHV = s.Stat("/dev/mshv").Exists
	return k
}

func normBool(v string) string {
	switch strings.ToUpper(v) {
	case "Y", "1":
		return "Y"
	case "N", "0":
		return "N"
	}
	return ""
}

func moduleAvailable(s sys.System, rel, mod string) (avail, builtin bool) {
	file := strings.ReplaceAll(mod, "_", "-") + ".ko"
	for _, root := range []string{"/lib/modules/", "/usr/lib/modules/"} {
		if hasModuleLine(read(s, root+rel+"/modules.dep"), file) {
			avail = true
		}
		if hasModuleLine(read(s, root+rel+"/modules.builtin"), file) {
			avail, builtin = true, true
		}
	}
	return
}

// hasModuleLine matches "kernel/arch/x86/kvm/kvm-intel.ko[.zst]: deps" (modules.dep)
// and bare "kernel/.../kvm-intel.ko" (modules.builtin) lines.
func hasModuleLine(content, file string) bool {
	for _, line := range strings.Split(content, "\n") {
		path, _, _ := strings.Cut(line, ":")
		if strings.HasPrefix(filepath.Base(strings.TrimSpace(path)), file) {
			return true
		}
	}
	return false
}

func kernelConfig(s sys.System, rel string) KernelConfig {
	var text, src string
	if b, err := s.ReadFile("/proc/config.gz"); err == nil {
		if zr, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
			if raw, err := io.ReadAll(io.LimitReader(zr, 8<<20)); err == nil {
				text, src = string(raw), "/proc/config.gz"
			}
		}
	}
	if text == "" {
		if t := read(s, "/boot/config-"+rel); t != "" {
			text, src = t, "/boot/config-"+rel
		}
	}
	kc := KernelConfig{Source: src}
	if text == "" {
		return kc
	}
	get := func(opt string) string {
		for _, l := range strings.Split(text, "\n") {
			if strings.HasPrefix(l, opt+"=") {
				return strings.TrimPrefix(l, opt+"=")
			}
		}
		return "n"
	}
	kc.KVM, kc.Intel, kc.AMD = get("CONFIG_KVM"), get("CONFIG_KVM_INTEL"), get("CONFIG_KVM_AMD")
	return kc
}

// ---- tools ----

func detectTools(s sys.System, arch string, d Distro) Tools {
	t := Tools{}
	names := []string{"qemu-system-" + arch}
	if arch == "x86_64" || arch == "aarch64" {
		names = append(names, "qemu-kvm")
	}
	for _, n := range names {
		for _, p := range s.LookPathAll(n) {
			if sys.IsShim(p) {
				t.ShimActive = true
				continue
			}
			t.QEMU = p
			break
		}
		if t.QEMU != "" {
			break
		}
	}
	if t.QEMU != "" {
		if out, _ := s.Exec(t.QEMU, "--version"); out != "" {
			t.QEMUVersion = strings.TrimPrefix(strings.SplitN(sys.Trim(out), "\n", 2)[0], "QEMU emulator version ")
		}
		out, err := s.Exec(t.QEMU, "-accel", "help")
		t.TCG = err != nil || strings.Contains(out, "tcg")
	}
	first := func(name string) string {
		for _, p := range s.LookPathAll(name) {
			if !sys.IsShim(p) {
				return p
			}
		}
		return ""
	}
	t.QEMUImg = first("qemu-img")
	t.Libvirt = first("virsh")
	t.Virtiofsd = first("virtiofsd")
	if t.Virtiofsd == "" && s.Stat("/usr/lib/virtiofsd").Exists {
		t.Virtiofsd = "/usr/lib/virtiofsd"
	}
	t.Docker, t.Podman = first("docker"), first("podman")
	t.UML = first("linux.uml")
	for _, pm := range []string{"apt-get", "dnf", "yum", "zypper", "pacman", "apk"} {
		if first(pm) != "" {
			t.PkgManager = pm
			break
		}
	}
	return t
}
