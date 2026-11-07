package pkgmgr

import (
	"strings"
	"testing"
)

func TestQEMUPackagesAndCommands(t *testing.T) {
	cases := []struct{ mgr, distro, arch, wantPkg, wantCmd string }{
		{"apt-get", "debian", "x86_64", "qemu-system-x86", "apt-get install -y --no-install-recommends qemu-system-x86"},
		{"apt-get", "ubuntu", "aarch64", "qemu-system-arm", "apt-get install"},
		{"dnf", "fedora", "x86_64", "qemu-system-x86-core", "dnf install -y"},
		{"dnf", "rocky", "x86_64", "qemu-kvm", "dnf install -y"},
		{"yum", "centos", "x86_64", "qemu-kvm", "yum install -y"},
		{"zypper", "opensuse-tumbleweed", "x86_64", "qemu-x86", "zypper --non-interactive install --no-recommends"},
		{"pacman", "arch", "x86_64", "qemu-system-x86", "pacman -S"},
		{"apk", "alpine", "aarch64", "qemu-system-aarch64", "apk add --no-cache"},
	}
	for _, c := range cases {
		pkgs, err := QEMUPackages(c.mgr, c.distro, c.arch)
		if err != nil || !strings.Contains(strings.Join(pkgs, " "), c.wantPkg) {
			t.Errorf("%s/%s/%s: pkgs %v err %v", c.mgr, c.distro, c.arch, pkgs, err)
			continue
		}
		var flat []string
		for _, cmd := range InstallCmds(c.mgr, pkgs, false) {
			flat = append(flat, strings.Join(cmd, " "))
		}
		if !strings.Contains(strings.Join(flat, "\n"), c.wantCmd) {
			t.Errorf("%s: commands %v lack %q", c.mgr, flat, c.wantCmd)
		}
		if rm := RemoveCmd(c.mgr); len(rm) == 0 {
			t.Errorf("%s: no remove command", c.mgr)
		}
	}
	if _, err := QEMUPackages("brew", "mac", "x86_64"); err == nil {
		t.Error("unsupported manager should error")
	}
	if _, err := QEMUPackages("apt-get", "debian", "riscv64"); err == nil {
		t.Error("unsupported arch should error")
	}
	if got := strings.Join(InstallCmds("pacman", []string{"x"}, true)[0], " "); !strings.Contains(got, "-Sy ") {
		t.Errorf("fresh pacman should refresh: %s", got)
	}
}
