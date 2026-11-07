// Package plan turns a detection report into an ordered list of reversible
// steps (the enablement ladder) and executes them with a journal.
package plan

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/KVM-X/KVM-X/internal/detect"
	"github.com/KVM-X/KVM-X/internal/guide"
	"github.com/KVM-X/KVM-X/internal/journal"
	"github.com/KVM-X/KVM-X/internal/ops"
	"github.com/KVM-X/KVM-X/internal/pkgmgr"
	"github.com/KVM-X/KVM-X/internal/sys"
)

type Options struct {
	GuestArch string // QEMU packages to install; default host arch
	NoQEMU    bool
	UML       bool // also install user-mode Linux (Debian only)
	NoNested  bool
	Shims     string // auto | yes | no
	Persist   bool
	Boot      bool   // boot-time subset: modules, device node, permissions only
	Binary    string // absolute path of kvmx, target of shims and the systemd unit
	ShimDir   string // default /usr/local/bin (or ~/.local/bin without root/sudo)
}

type Step struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Ops   []ops.Op `json:"-"`
}

type Plan struct {
	Steps  []Step         `json:"steps"`
	Manual []guide.Action `json:"manual"`
	Notes  []string       `json:"notes"`
	// Expect is the status the machine should reach once the plan has run.
	Expect detect.Mode `json:"expected_mode"`
}

func (p *Plan) NeedsRoot() bool {
	for _, st := range p.Steps {
		for _, o := range st.Ops {
			if o.NeedsRoot() {
				return true
			}
		}
	}
	return false
}

const (
	modprobeConf = "/etc/modprobe.d/kvmx.conf"
	modulesLoad  = "/etc/modules-load.d/kvmx.conf"
	udevRule     = "/etc/udev/rules.d/65-kvmx.rules"
	tmpfilesConf = "/etc/tmpfiles.d/kvmx.conf"
	unitPath     = "/etc/systemd/system/kvmx.service"
)

// Unit is the systemd unit that re-applies device/module setup at boot.
func Unit(binary string) string {
	return fmt.Sprintf(`[Unit]
Description=KVM-X: restore KVM device and module setup at boot
After=local-fs.target systemd-modules-load.service
Wants=systemd-modules-load.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%s enable --boot --yes

[Install]
WantedBy=multi-user.target
`, binary)
}

func groupExists(s sys.System, name string) bool {
	b, err := s.ReadFile("/etc/group")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, name+":") {
			return true
		}
	}
	return false
}

func have(s sys.System, name string) bool { return len(s.LookPathAll(name)) > 0 }

// Build derives the plan for a report.
func Build(s sys.System, r *detect.Report, o Options) *Plan {
	p := &Plan{Expect: r.Mode}
	if !r.KVM.Usable {
		p.Expect = detect.ModeTCG
	}
	add := func(id, title string, list ...ops.Op) {
		if len(list) > 0 {
			p.Steps = append(p.Steps, Step{ID: id, Title: title, Ops: list})
		}
	}
	k := r.KVM
	nestedMod := k.Module == "kvm_intel" || k.Module == "kvm_amd"
	inContainer := r.Virt.IsContainer
	fixable := func(id string) bool { b, ok := r.Blocker(id); return ok && b.Fixable }
	needLoad := fixable(detect.BModuleNotLoaded)
	needMknod := fixable(detect.BDevMissing) || fixable(detect.BContainerNoDev)
	user := r.Sandbox.User

	// 1. nested=1 for the module (persistent) and reload if idle.
	if !o.NoNested && nestedMod && k.ModuleAvailable && k.NestedParam != "Y" && !inContainer {
		conf := fmt.Sprintf("# managed by kvmx; undo with `kvmx revert`\noptions %s nested=1\n", k.Module)
		add("nested-modprobe-conf", "Make VMs hosted here able to run KVM (nested=1)",
			ops.WriteFile{Path: modprobeConf, Content: conf, Mode: 0o644})
		if k.ModuleLoaded && k.NestedParam == "N" {
			if k.ModuleRefs == 0 {
				add("reload-kvm-module", "Reload "+k.Module+" so nested=1 takes effect now",
					ops.Run{Argv: []string{"modprobe", "-r", k.Module}, Priv: true},
					ops.Run{Argv: []string{"modprobe", k.Module}, Priv: true, Note: "kvm module stays loaded with nested=1 until reboot or `modprobe -r " + k.Module + "`"})
			} else {
				p.Notes = append(p.Notes, fmt.Sprintf("%s is in use (refcount %d): nested=1 will apply after a reboot or once VMs stop.", k.Module, k.ModuleRefs))
			}
		}
	}

	// 2. load the module now and on every boot.
	if needLoad {
		add("load-kvm-module", "Load "+k.Module,
			ops.Run{Argv: []string{"modprobe", k.Module}, Priv: true, Note: k.Module + " left loaded; unload with `modprobe -r " + k.Module + "` if desired"},
			ops.WriteFile{Path: modulesLoad, Content: "# managed by kvmx\n" + k.Module + "\n", Mode: 0o644})
	}

	// 3. device node.
	if needMknod {
		minor := 232 // KVM_MINOR is fixed in the kernel ABI
		if k.MiscMinor > 0 {
			minor = k.MiscMinor
		}
		group := ""
		if groupExists(s, "kvm") {
			group = "kvm"
		}
		st := []ops.Op{ops.Mknod{Path: "/dev/kvm", Major: 10, Minor: minor, Mode: "0660", Group: group}}
		if o.Persist {
			st = append(st, ops.WriteFile{Path: tmpfilesConf, Content: fmt.Sprintf("# managed by kvmx\nc! /dev/kvm 0660 root kvm - 10:%d\n", minor), Mode: 0o644})
		}
		add("mknod-dev-kvm", "Create /dev/kvm", st...)
		if inContainer {
			p.Notes = append(p.Notes, "Inside a container the device cgroup may still deny access to the new node; if so pass the device from the host: `kvmx flags docker`.")
		}
	}

	// 4. permissions for the target user.
	needPerm := fixable(detect.BDevPermission) ||
		((needLoad || needMknod) && r.Sandbox.UID != 0 && user != "" && !k.InKVMGroupDB)
	if needPerm && user != "" && user != "root" {
		var st []ops.Op
		if !groupExists(s, "kvm") {
			if have(s, "groupadd") {
				st = append(st, ops.Run{Argv: []string{"groupadd", "-r", "kvm"}, UndoArgv: []string{"groupdel", "kvm"}, Priv: true})
			} else {
				st = append(st, ops.Run{Argv: []string{"addgroup", "-S", "kvm"}, UndoArgv: []string{"delgroup", "kvm"}, Priv: true})
			}
		}
		if !k.InKVMGroupDB {
			if have(s, "usermod") {
				st = append(st, ops.Run{Argv: []string{"usermod", "-aG", "kvm", user}, UndoArgv: []string{"gpasswd", "-d", user, "kvm"}, Priv: true})
			} else {
				st = append(st, ops.Run{Argv: []string{"addgroup", user, "kvm"}, UndoArgv: []string{"delgroup", user, "kvm"}, Priv: true})
			}
		}
		st = append(st, ops.WriteFile{Path: udevRule, Mode: 0o644,
			Content: "# managed by kvmx\nKERNEL==\"kvm\", GROUP=\"kvm\", MODE=\"0660\", OPTIONS+=\"static_node=kvm\"\n"})
		if have(s, "setfacl") {
			st = append(st, ops.Run{Argv: []string{"setfacl", "-m", "u:" + user + ":rw", "/dev/kvm"},
				UndoArgv: []string{"setfacl", "-x", "u:" + user, "/dev/kvm"}, Priv: true, IfExists: "/dev/kvm"})
		} else {
			p.Notes = append(p.Notes, "Group membership applies at next login; run `newgrp kvm` (or log out and in) to use /dev/kvm in this session.")
		}
		add("kvm-access", "Let "+user+" open /dev/kvm (group kvm, udev rule, ACL)", st...)
	}
	if o.Boot {
		return p
	}

	// 5. QEMU for KVM and the TCG fallback.
	if !o.NoQEMU && r.HasBlocker(detect.BQEMUMissing) && r.Tools.PkgManager != "" {
		arch := o.GuestArch
		if arch == "" {
			arch = r.CPU.Arch
		}
		if pkgs, err := pkgmgr.QEMUPackages(r.Tools.PkgManager, r.Distro.ID, arch); err != nil {
			p.Notes = append(p.Notes, err.Error())
		} else {
			add("install-qemu", "Install QEMU ("+strings.Join(pkgs, ", ")+") via "+r.Tools.PkgManager,
				ops.PkgInstall{Cmds: pkgmgr.InstallCmds(r.Tools.PkgManager, pkgs, !s.Stat("/var/lib/pacman/sync/core.db").Exists), Pkgs: pkgs,
					RemoveCmd: pkgmgr.RemoveCmd(r.Tools.PkgManager), Installed: pkgmgr.Installed(s, r.Tools.PkgManager)})
		}
	}

	// 5b. user-mode Linux, the non-VM rung that runs Linux guests at native CPU speed.
	if o.UML && r.Tools.UML == "" && r.Tools.PkgManager != "" {
		if pkgs, err := pkgmgr.UMLPackages(r.Tools.PkgManager, r.Distro.ID); err != nil {
			p.Notes = append(p.Notes, err.Error())
		} else {
			add("install-uml", "Install user-mode Linux ("+strings.Join(pkgs, ", ")+")",
				ops.PkgInstall{Cmds: pkgmgr.InstallCmds(r.Tools.PkgManager, pkgs, false), Pkgs: pkgs,
					RemoveCmd: pkgmgr.RemoveCmd(r.Tools.PkgManager), Installed: pkgmgr.Installed(s, r.Tools.PkgManager)})
		}
	}

	// 6. compatibility shims.
	wantShims := o.Shims == "yes" || (o.Shims != "no" && p.Expect == detect.ModeTCG)
	if wantShims && o.Binary != "" {
		dir, user := o.ShimDir, false
		if dir == "" {
			dir = "/usr/local/bin"
			if r.Sandbox.UID != 0 && !r.Sandbox.HasSudo {
				home, _ := os.UserHomeDir()
				dir, user = filepath.Join(home, ".local", "bin"), true
			}
		}
		names := []string{"qemu-system-" + r.CPU.Arch}
		if r.CPU.Arch == "x86_64" {
			names = append(names, "kvm")
		}
		names = append(names, "virsh", "virt-install")
		var sl []ops.Op
		for _, n := range names {
			if (n == "virsh" || n == "virt-install") && !have(s, n) {
				continue
			}
			sl = append(sl, ops.Symlink{Path: filepath.Join(dir, n), Target: o.Binary, User: user})
		}
		add("install-shims", "Install qemu/kvm/virsh compatibility shims in "+dir, sl...)
		if user {
			p.Notes = append(p.Notes, "Put "+dir+" first in PATH so the shims take precedence.")
		}
	}

	// 7. persistence across reboots.
	if o.Persist && o.Binary != "" {
		if s.Stat("/run/systemd/system").Exists {
			add("systemd-persist", "Restore KVM setup at every boot (kvmx.service)",
				ops.WriteFile{Path: unitPath, Content: Unit(o.Binary), Mode: 0o644},
				ops.Run{Argv: []string{"systemctl", "daemon-reload"}, Priv: true},
				ops.Run{Argv: []string{"systemctl", "enable", "kvmx.service"}, UndoArgv: []string{"systemctl", "disable", "kvmx.service"}, Priv: true})
		} else {
			p.Notes = append(p.Notes, "systemd is not running here; add `"+o.Binary+" enable --boot --yes` to your init system's boot scripts.")
		}
	}

	p.Manual = guide.Manual(r)
	return p
}

// Print renders the plan so the user sees exactly what will change.
func (p *Plan) Print(w io.Writer, sudo bool) {
	if len(p.Steps) == 0 {
		fmt.Fprintln(w, "Nothing to change.")
	}
	for i, st := range p.Steps {
		fmt.Fprintf(w, "[%d/%d] %s  (%s)\n", i+1, len(p.Steps), st.Title, st.ID)
		for _, o := range st.Ops {
			fmt.Fprintf(w, "      %s\n", strings.ReplaceAll(o.Describe(sudo && o.NeedsRoot()), "\n", "\n      "))
		}
	}
	for _, n := range p.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}

// Execute applies the plan, journaling each change. A failing step is
// reported and skipped; later independent steps still run. It returns the
// number of failed steps.
func Execute(p *Plan, env *ops.Env, j *journal.Journal, w io.Writer) int {
	failed := 0
	for i, st := range p.Steps {
		fmt.Fprintf(w, "[%d/%d] %s\n", i+1, len(p.Steps), st.Title)
		for _, o := range st.Ops {
			undo, err := o.Apply(env)
			if undo.Kind != "none" || undo.Note != "" {
				if _, jerr := j.Append(journal.Entry{Step: st.ID, Desc: firstLine(o.Describe(false)), Undo: undo}); jerr != nil {
					fmt.Fprintf(w, "  warning: could not journal change: %v\n", jerr)
				}
			}
			if err != nil {
				fmt.Fprintf(w, "  FAILED: %v\n", err)
				failed++
				break
			}
		}
	}
	return failed
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }
