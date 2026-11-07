// Package sys abstracts everything kvmx reads from the machine, so detection
// logic can run against recorded fixtures as well as the real host.
package sys

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// FileStat describes a path. Device numbers are only set for char devices.
type FileStat struct {
	Exists       bool
	Mode         fs.FileMode
	IsChar       bool
	Major, Minor uint32
	UID, GID     uint32
	Group        string
}

// KVMProbe is the result of actually exercising /dev/kvm.
type KVMProbe struct {
	Opened     bool   `json:"opened"`
	OpenErr    string `json:"open_error,omitempty"`
	APIVersion int    `json:"api_version"`
	APIErr     string `json:"api_error,omitempty"`
	CreateVMOK bool   `json:"create_vm_ok"`
	CreateErr  string `json:"create_vm_error,omitempty"`
}

// System is the read-only view of the machine used by detection.
type System interface {
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]string, error)
	Stat(path string) FileStat
	// Exec runs a read-only probe command and returns stdout even on non-zero exit.
	Exec(name string, args ...string) (string, error)
	// LookPathAll returns every executable called name on PATH (plus sbin dirs), in order.
	LookPathAll(name string) []string
	Getenv(key string) string
	UID() int
	// TargetUser is the human the machine is being set up for (SUDO_USER if set).
	TargetUser() string
	// ProcessGIDs are the groups the running process actually holds.
	ProcessGIDs() []int
	// MemberGroups are the groups the target user belongs to per the group database.
	MemberGroups() []string
	ProbeKVM(path string) KVMProbe
	GOARCH() string
	// HTTPGet is restricted to the link-local cloud metadata address.
	HTTPGet(method, rawurl string, headers map[string]string) (string, error)
}

// Host is the real machine.
type Host struct{}

const (
	kvmGetAPIVersion = 0xAE00
	kvmCreateVM      = 0xAE01
	// MetadataHost is the only host HTTPGet will talk to.
	MetadataHost = "169.254.169.254"
)

var extraDirs = []string{"/usr/local/sbin", "/usr/sbin", "/sbin", "/usr/libexec", "/usr/local/libexec"}

func (Host) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) }

func (Host) ReadDir(p string) ([]string, error) {
	es, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(es))
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names, nil
}

func (Host) Stat(p string) FileStat {
	fi, err := os.Stat(p)
	if err != nil {
		return FileStat{}
	}
	fs := FileStat{Exists: true, Mode: fi.Mode()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		fs.UID, fs.GID = st.Uid, st.Gid
		if fi.Mode()&os.ModeCharDevice != 0 {
			fs.IsChar = true
			// Linux dev_t layout (glibc major()/minor()).
			rdev := uint64(st.Rdev) //nolint:unconvert // Rdev width differs per arch
			fs.Major = uint32(((rdev >> 8) & 0xfff) | ((rdev >> 32) &^ 0xfff))
			fs.Minor = uint32((rdev & 0xff) | ((rdev >> 12) &^ 0xff))
		}
		if g, err := user.LookupGroupId(itoa(st.Gid)); err == nil {
			fs.Group = g.Name
		}
	}
	return fs
}

func (Host) Exec(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

func (Host) LookPathAll(name string) []string {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	dirs = append(dirs, extraDirs...)
	seen := map[string]bool{}
	var found []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name)
		if seen[p] {
			continue
		}
		seen[p] = true
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			found = append(found, p)
		}
	}
	return found
}

func (Host) Getenv(k string) string { return os.Getenv(k) }
func (Host) UID() int               { return os.Geteuid() }
func (Host) GOARCH() string         { return runtime.GOARCH }

func (h Host) TargetUser() string {
	if su := os.Getenv("SUDO_USER"); su != "" && os.Geteuid() == 0 {
		return su
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func (Host) ProcessGIDs() []int {
	gids, _ := os.Getgroups()
	return append(gids, os.Getgid())
}

func (h Host) MemberGroups() []string {
	u, err := user.Lookup(h.TargetUser())
	if err != nil {
		return nil
	}
	ids, err := u.GroupIds()
	if err != nil {
		return nil
	}
	var names []string
	for _, id := range ids {
		if g, err := user.LookupGroupId(id); err == nil {
			names = append(names, g.Name)
		}
	}
	return names
}

func (Host) ProbeKVM(path string) KVMProbe {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return KVMProbe{OpenErr: errText(err)}
	}
	defer f.Close()
	p := KVMProbe{Opened: true}
	v, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), kvmGetAPIVersion, 0)
	if e != 0 {
		p.APIErr = e.Error()
		return p
	}
	p.APIVersion = int(v)
	vm, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), kvmCreateVM, 0)
	if e != 0 {
		p.CreateErr = e.Error()
		return p
	}
	_ = syscall.Close(int(vm))
	p.CreateVMOK = true
	return p
}

// HTTPGet performs a request against the cloud metadata address only, with a
// short timeout. It exists so `--cloud-metadata` is the single, opt-in network
// probe kvmx makes besides downloads the user confirms.
func (Host) HTTPGet(method, raw string, headers map[string]string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != MetadataHost {
		return "", errors.New("refusing non-metadata URL")
	}
	client := &http.Client{
		Timeout:   1500 * time.Millisecond,
		Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, Proxy: nil},
	}
	req, err := http.NewRequest(method, raw, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(http.MaxBytesReader(nil, resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return buf.String(), errors.New(resp.Status)
	}
	return buf.String(), nil
}

func errText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func itoa(n uint32) string {
	var b [10]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			break
		}
	}
	return string(b[i:])
}

// Trim is a small helper shared by probes.
func Trim(s string) string { return strings.TrimSpace(s) }

// IsShim reports whether path is a kvmx shim (a symlink that resolves to a
// binary called kvmx), so lookups can skip it and find the real tool.
func IsShim(path string) bool {
	r, err := filepath.EvalSymlinks(path)
	return err == nil && filepath.Base(r) == "kvmx"
}
