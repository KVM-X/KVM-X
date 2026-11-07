// Package guest fetches a tiny, hash-pinned Alpine kernel + initramfs and
// layers a test /init on top, so kvmx can boot a real Linux guest anywhere to
// prove the TCG fallback works and to benchmark it.
package guest

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AlpineVersion is the pinned release the assets come from.
const AlpineVersion = "3.24.4"

type Asset struct {
	Name   string
	URL    string
	SHA256 string
	Size   int64
}

func base(arch string) string {
	return "https://dl-cdn.alpinelinux.org/alpine/v" + AlpineVersion[:strings.LastIndex(AlpineVersion, ".")] +
		"/releases/" + arch + "/netboot/"
}

// Assets returns the pinned files for a guest architecture.
func Assets(arch string) ([]Asset, error) {
	switch arch {
	case "x86_64":
		return []Asset{
			{"vmlinuz-virt", base(arch) + "vmlinuz-virt", "1e8ac7aebfab3f0fcb443eae94ad320efbbda51be91b928c32ca1740b44ebe9d", 12649472},
			{"initramfs-virt", base(arch) + "initramfs-virt", "cf39ceb8bd232584851218bf827668e07818cba82aeea784b1cc03969d01c3a5", 9660834},
		}, nil
	case "aarch64":
		return []Asset{
			{"vmlinuz-virt", base(arch) + "vmlinuz-virt", "cd05fed9e617b5df93dc62ece9b78c422fd94e9b432a2f7c092714b139d5eef4", 10412544},
			{"initramfs-virt", base(arch) + "initramfs-virt", "189402937646965374ef2df5d37f76467135179ae168c88175df2fe3e264a0b0", 9404792},
		}, nil
	}
	return nil, fmt.Errorf("no test guest for architecture %q", arch)
}

// CacheDir is where downloaded assets live.
func CacheDir() string {
	if d := os.Getenv("KVMX_CACHE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "kvmx")
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "kvmx-cache")
	}
	return filepath.Join(h, ".cache", "kvmx")
}

type EnsureOpts struct {
	Yes   bool // download without asking
	TTY   bool // may prompt
	Out   io.Writer
	In    io.Reader
	Offer func() // optional hook, unused when nil
}

type Paths struct{ Kernel, Initrd string }

func fileOK(path, want string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == want
}

// fetchMissing downloads whichever of as are absent or fail verification in dir.
// It never downloads without showing the URLs and, unless Yes is set, asking.
func fetchMissing(as []Asset, dir string, o EnsureOpts) error {
	var missing []Asset
	for _, a := range as {
		if !fileOK(filepath.Join(dir, a.Name), a.SHA256) {
			missing = append(missing, a)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var total int64
	fmt.Fprintln(o.Out, "kvmx needs these files for the test guest (sha256-pinned, from the Alpine CDN):")
	for _, a := range missing {
		fmt.Fprintf(o.Out, "  %s  (%.1f MB)\n", a.URL, float64(a.Size)/1e6)
		total += a.Size
	}
	switch {
	case o.Yes:
	case o.TTY:
		fmt.Fprintf(o.Out, "Download %.1f MB into %s? [y/N] ", float64(total)/1e6, dir)
		ans, _ := bufio.NewReader(o.In).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			return errors.New("download declined")
		}
	default:
		return errors.New("test guest is not cached; re-run with --yes to allow the download above")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, a := range missing {
		if err := download(a, filepath.Join(dir, a.Name)); err != nil {
			return err
		}
	}
	return nil
}

// Ensure makes sure the kernel and initramfs are cached and verified.
func Ensure(arch string, o EnsureOpts) (Paths, error) {
	as, err := Assets(arch)
	if err != nil {
		return Paths{}, err
	}
	dir := filepath.Join(CacheDir(), "guest", "alpine-"+AlpineVersion, arch)
	if err := fetchMissing(as, dir, o); err != nil {
		return Paths{}, err
	}
	return Paths{Kernel: filepath.Join(dir, "vmlinuz-virt"), Initrd: filepath.Join(dir, "initramfs-virt")}, nil
}

// Rootfs returns a directory holding an extracted Alpine mini root filesystem
// (for user-mode Linux, which boots from a host directory). x86_64 only.
func Rootfs(arch string, o EnsureOpts) (string, error) {
	if arch != "x86_64" {
		return "", fmt.Errorf("the user-mode Linux demo rootfs is x86_64 only (host arch %s)", arch)
	}
	a := Asset{"alpine-minirootfs.tar.gz",
		"https://dl-cdn.alpinelinux.org/alpine/v" + AlpineVersion[:strings.LastIndex(AlpineVersion, ".")] + "/releases/x86_64/alpine-minirootfs-" + AlpineVersion + "-x86_64.tar.gz",
		"cdaa38cb8de3537967ee5b6c9b6cd00cc962da2b018e6eea48696bb274923012", 3_900_000}
	dir := filepath.Join(CacheDir(), "guest", "alpine-"+AlpineVersion, "x86_64-rootfs")
	if err := fetchMissing([]Asset{a}, dir, o); err != nil {
		return "", err
	}
	root := filepath.Join(dir, "root")
	if _, err := os.Stat(filepath.Join(root, ".kvmx-extracted")); err == nil {
		return root, nil
	}
	if err := extractTarGz(filepath.Join(dir, a.Name), root); err != nil {
		return "", err
	}
	return root, os.WriteFile(filepath.Join(root, ".kvmx-extracted"), nil, 0o644)
}

func extractTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.Clean("/"+h.Name))
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				err = writeEntry(target, tr, h.FileInfo().Mode().Perm())
			}
		case tar.TypeSymlink:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				_ = os.Remove(target)
				err = os.Symlink(h.Linkname, target)
			}
		case tar.TypeLink:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				_ = os.Remove(target)
				err = os.Link(filepath.Join(dest, filepath.Clean("/"+h.Linkname)), target)
			}
		}
		if err != nil {
			return err
		}
	}
}

func writeEntry(path string, r io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o200)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil { //nolint:gosec // archive is sha256-pinned and tiny
		_ = out.Close()
		return err
	}
	return out.Close()
}

func download(a Asset, dst string) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(a.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", a.URL, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".dl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return fmt.Errorf("%s: sha256 mismatch (got %s, want %s)", a.Name, got, a.SHA256)
	}
	return os.Rename(tmp.Name(), dst)
}

// InitScript is the guest /init: print markers on the serial console, run an
// optional CPU benchmark (kvmx.bench=1 on the kernel command line), power off.
const InitScript = `#!/bin/sh
/bin/busybox --install -s /bin 2>/dev/null
export PATH=/bin:/sbin:/usr/bin:/usr/sbin
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev 2>/dev/null
up() { cut -d' ' -f1 /proc/uptime; }
echo "KVMX-BOOT-OK kernel=$(uname -r) arch=$(uname -m) uptime=$(up)"
if grep -q 'kvmx.bench=1' /proc/cmdline; then
  t0=$(up); i=0
  while [ $i -lt 100000 ]; do i=$((i+1)); done
  t1=$(up)
  echo "KVMX-BENCH shell_loop_s=$(awk "BEGIN{printf \"%.2f\", $t1-$t0}")"
  t0=$(up)
  dd if=/dev/zero bs=1M count=16 2>/dev/null | sha256sum >/dev/null
  t1=$(up)
  echo "KVMX-BENCH sha256_16mb_s=$(awk "BEGIN{printf \"%.2f\", $t1-$t0}")"
  n=$(grep -c '^processor' /proc/cpuinfo)
  t0=$(up); j=0
  while [ $j -lt "$n" ]; do
    ( i=0; while [ $i -lt 50000 ]; do i=$((i+1)); done ) &
    j=$((j+1))
  done
  wait
  t1=$(up)
  echo "KVMX-BENCH parallel_loop_s=$(awk "BEGIN{printf \"%.2f\", $t1-$t0}") vcpus=$n"
  t0=$(up)
  dd if=/dev/zero of=/dev/null bs=1 count=100000 2>/dev/null
  t1=$(up)
  echo "KVMX-BENCH syscalls_s=$(awk "BEGIN{printf \"%.2f\", $t1-$t0}")"
fi
echo "KVMX-DONE"
poweroff -f
echo o > /proc/sysrq-trigger
sleep 30
`

// Overlay appends a cpio archive containing /init to the base initramfs. The
// kernel unpacks concatenated archives in order, so ours wins.
func Overlay(baseInitrd, script, outDir string) (string, error) {
	sum := sha256.Sum256([]byte(script + "|" + baseInitrd))
	out := filepath.Join(outDir, "initrd-"+hex.EncodeToString(sum[:6])+".img")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	b, err := os.ReadFile(baseInitrd)
	if err != nil {
		return "", err
	}
	for len(b)%4 != 0 { // the kernel only starts a raw cpio on a 4-byte boundary
		b = append(b, 0)
	}
	b = append(b, CPIO([]CPIOEntry{{Name: "init", Mode: 0o100755, Data: []byte(script)}})...)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", err
	}
	return out, os.Rename(tmp, out)
}

type CPIOEntry struct {
	Name string
	Mode uint32
	Data []byte
}

// CPIO writes a newc-format archive.
func CPIO(entries []CPIOEntry) []byte {
	var buf bytes.Buffer
	pad := func() {
		for buf.Len()%4 != 0 {
			buf.WriteByte(0)
		}
	}
	write := func(name string, mode uint32, data []byte, ino int) {
		fmt.Fprintf(&buf, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
			ino, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(name)+1, 0)
		buf.WriteString(name)
		buf.WriteByte(0)
		pad()
		buf.Write(data)
		pad()
	}
	for i, e := range entries {
		write(e.Name, e.Mode, e.Data, i+1)
	}
	write("TRAILER!!!", 0, nil, 0)
	return buf.Bytes()
}
