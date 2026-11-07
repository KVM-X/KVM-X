// Package pkgmgr maps "install QEMU" onto apt, dnf, yum, zypper, pacman and apk.
package pkgmgr

import (
	"fmt"
	"strings"

	"github.com/KVM-X/KVM-X/internal/sys"
)

// Supported managers, in detection preference order.
var Supported = []string{"apt-get", "dnf", "yum", "zypper", "pacman", "apk"}

// QEMUPackages returns the package names that provide a system emulator for
// guestArch (x86_64 or aarch64) plus qemu-img, with the BIOS files QEMU needs
// for direct-kernel and disk boots.
func QEMUPackages(manager, distroID, guestArch string) ([]string, error) {
	x86 := guestArch == "x86_64"
	if !x86 && guestArch != "aarch64" {
		return nil, fmt.Errorf("no QEMU package mapping for guest architecture %q", guestArch)
	}
	pick := func(a, b []string) []string {
		if x86 {
			return a
		}
		return b
	}
	switch manager {
	case "apt-get":
		return pick([]string{"qemu-system-x86", "qemu-utils", "seabios"}, []string{"qemu-system-arm", "qemu-utils", "qemu-efi-aarch64"}), nil
	case "dnf", "yum":
		if distroID == "fedora" {
			return pick([]string{"qemu-system-x86-core", "qemu-img"}, []string{"qemu-system-aarch64-core", "qemu-img"}), nil
		}
		return []string{"qemu-kvm", "qemu-img"}, nil // RHEL family ships one multi-purpose package per host arch
	case "zypper":
		return pick([]string{"qemu-x86", "qemu-tools", "qemu-seabios"}, []string{"qemu-arm", "qemu-tools"}), nil
	case "pacman":
		return pick([]string{"qemu-system-x86", "qemu-img"}, []string{"qemu-system-aarch64", "qemu-img"}), nil
	case "apk":
		return pick([]string{"qemu-system-x86_64", "qemu-img"}, []string{"qemu-system-aarch64", "qemu-img"}), nil
	}
	return nil, fmt.Errorf("unsupported package manager %q", manager)
}

// InstallCmds returns the commands (run as root, in order) that install pkgs.
// refresh asks pacman to download its package database first, needed only on
// a system whose sync databases were never fetched (fresh images).
func InstallCmds(manager string, pkgs []string, refresh bool) [][]string {
	with := func(prefix ...string) []string { return append(append([]string{}, prefix...), pkgs...) }
	switch manager {
	case "apt-get":
		return [][]string{
			{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update"},
			with("env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--no-install-recommends"),
		}
	case "dnf":
		return [][]string{with("dnf", "install", "-y", "--setopt=install_weak_deps=False")}
	case "yum":
		return [][]string{with("yum", "install", "-y")}
	case "zypper":
		return [][]string{with("zypper", "--non-interactive", "install", "--no-recommends")}
	case "pacman":
		// Arch does not support refreshing the database without a full upgrade; only do it
		// when there is no database at all (fresh image). Otherwise run `pacman -Syu` yourself if this 404s.
		if refresh {
			return [][]string{with("pacman", "-Sy", "--noconfirm", "--needed")}
		}
		return [][]string{with("pacman", "-S", "--noconfirm", "--needed")}
	case "apk":
		return [][]string{with("apk", "add", "--no-cache")}
	}
	return nil
}

// RemoveCmd is the command prefix that removes packages (packages are appended).
func RemoveCmd(manager string) []string {
	switch manager {
	case "apt-get":
		return []string{"apt-get", "remove", "-y"}
	case "dnf":
		return []string{"dnf", "remove", "-y"}
	case "yum":
		return []string{"yum", "remove", "-y"}
	case "zypper":
		return []string{"zypper", "--non-interactive", "remove"}
	case "pacman":
		return []string{"pacman", "-R", "--noconfirm"}
	case "apk":
		return []string{"apk", "del"}
	}
	return nil
}

// Installed asks the package database whether pkg is already present.
func Installed(s sys.System, manager string) func(string) bool {
	return func(pkg string) bool {
		var out string
		var err error
		switch manager {
		case "apt-get":
			out, err = s.Exec("dpkg-query", "-W", "-f=${Status}", pkg)
			return err == nil && strings.Contains(out, "install ok installed")
		case "dnf", "yum", "zypper":
			_, err = s.Exec("rpm", "-q", pkg)
		case "pacman":
			_, err = s.Exec("pacman", "-Q", pkg)
		case "apk":
			_, err = s.Exec("apk", "info", "-e", pkg)
		default:
			return false
		}
		return err == nil
	}
}

// UMLPackages returns the packages providing a user-mode Linux kernel. Only
// Debian ships one (12 and 13); other distributions need a kernel built with ARCH=um.
func UMLPackages(manager, distroID string) ([]string, error) {
	if manager == "apt-get" && distroID == "debian" {
		return []string{"user-mode-linux"}, nil
	}
	return nil, fmt.Errorf("no user-mode Linux package for %s/%s (Debian only); build a kernel with `make ARCH=um` instead", manager, distroID)
}
