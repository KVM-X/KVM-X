package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/KVM-X/KVM-X/internal/guide"
	"github.com/KVM-X/KVM-X/internal/qemu"
)

func (a *app) cmdFlags(args []string) int {
	var c common
	fs := a.flagSet("flags", &c)
	if code, ok := parse(fs, args); !ok {
		return code
	}
	kind := "docker"
	if fs.NArg() > 0 {
		kind = fs.Arg(0)
	}
	_, r, err := a.collect(&c, false)
	if err != nil {
		return a.fail("%v", err)
	}
	out := guide.Flags(kind, r)
	if strings.HasPrefix(out, "unknown runtime") {
		fmt.Fprintln(a.err, "kvmx: "+out)
		return exitUsage
	}
	if c.json {
		a.emit(map[string]string{"runtime": kind, "flags": out})
		return exitOK
	}
	fmt.Fprintln(a.out, out)
	return exitOK
}

func (a *app) cmdLibvirt(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.err, "usage: kvmx libvirt xml [flags]            print a libvirt domain (type='qemu' when KVM is unavailable)\n"+
			"       kvmx libvirt convert [FILE|-] [flags]  rewrite a KVM domain XML so it runs under TCG\n"+
			"         --domain NAME [--apply]                rewrite an existing libvirt domain via virsh")
		return exitUsage
	}
	switch args[0] {
	case "xml":
		return a.libvirtXML(args[1:])
	case "convert":
		return a.libvirtConvert(args[1:])
	}
	fmt.Fprintf(a.err, "kvmx libvirt: unknown subcommand %q\n", args[0])
	return exitUsage
}

func (a *app) libvirtXML(args []string) int {
	var c common
	var o qemu.DomainOpts
	var mode, mem string
	fs := a.flagSet("libvirt xml", &c)
	fs.StringVar(&o.Name, "name", "kvmx-vm", "domain name")
	fs.StringVar(&o.Arch, "arch", "", "x86_64 or aarch64 (default: this machine's)")
	fs.StringVar(&mode, "mode", "auto", "auto | kvm | tcg")
	fs.StringVar(&mem, "mem", "1G", "memory")
	fs.IntVar(&o.CPUs, "cpus", 2, "vCPUs")
	fs.StringVar(&o.Disk, "disk", "", "disk image")
	fs.StringVar(&o.Kernel, "kernel", "", "direct-boot kernel")
	fs.StringVar(&o.Initrd, "initrd", "", "initramfs")
	fs.StringVar(&o.Cmdline, "cmdline", "", "kernel command line")
	fs.IntVar(&o.TBCacheMB, "tb-cache", 0, "TCG translation cache in MiB (<tb-cache>, libvirt >= 8.0); 0 omits")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	_, r, err := a.collect(&c, false)
	if err != nil {
		return a.fail("%v", err)
	}
	if o.Arch == "" {
		o.Arch = r.CPU.Arch
	}
	if o.MemMB, err = parseMem(mem); err != nil {
		return a.fail("%v", err)
	}
	switch mode {
	case "auto":
		o.Mode = "tcg"
		if r.KVM.Usable && o.Arch == r.CPU.Arch {
			o.Mode = "kvm"
		}
	case "kvm", "tcg":
		o.Mode = mode
	default:
		return a.fail("--mode must be auto, kvm or tcg")
	}
	fmt.Fprint(a.out, qemu.GenerateDomainXML(o))
	if o.Mode == "tcg" {
		fmt.Fprintln(a.err, "kvmx: domain generated as type='qemu' (software emulation, TCG)")
	}
	return exitOK
}

func (a *app) libvirtConvert(args []string) int {
	var c common
	var domain string
	var apply bool
	fs := a.flagSet("libvirt convert", &c)
	fs.StringVar(&domain, "domain", "", "existing libvirt domain to rewrite (uses virsh dumpxml / define)")
	fs.BoolVar(&apply, "apply", false, "with --domain: redefine the domain with the rewritten XML")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	var in []byte
	var err error
	virsh := realTool("virsh")
	switch {
	case domain != "":
		if virsh == "" {
			return a.fail("virsh not found")
		}
		in, err = exec.Command(virsh, "dumpxml", "--inactive", domain).Output()
	case fs.NArg() == 0 || fs.Arg(0) == "-":
		in, err = io.ReadAll(a.in)
	default:
		in, err = os.ReadFile(fs.Arg(0))
	}
	if err != nil {
		return a.fail("%v", err)
	}
	out, changes := qemu.RewriteDomainXML(string(in))
	if len(changes) == 0 {
		fmt.Fprintln(a.err, "kvmx: nothing to change; the domain does not need KVM-only settings")
	}
	for _, ch := range changes {
		fmt.Fprintf(a.err, "kvmx: %s\n", ch)
	}
	if domain != "" && apply && !c.dry {
		tmp, err := os.CreateTemp("", "kvmx-domain-*.xml")
		if err != nil {
			return a.fail("%v", err)
		}
		defer os.Remove(tmp.Name())
		_, _ = tmp.WriteString(out)
		_ = tmp.Close()
		if b, err := exec.Command(virsh, "define", tmp.Name()).CombinedOutput(); err != nil {
			return a.fail("virsh define: %v: %s", err, b)
		}
		fmt.Fprintf(a.out, "domain %s redefined for software emulation\n", domain)
		return exitOK
	}
	fmt.Fprint(a.out, out)
	return exitOK
}
