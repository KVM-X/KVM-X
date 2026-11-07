package sys

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Fixture is a System backed by a recorded directory:
//
//	<dir>/root/...    mirror of /proc, /sys, /etc, /lib/modules ... files
//	<dir>/meta.json   things files cannot express (exec output, device nodes, uid)
//
// It powers the unit tests and the hidden `--fixture` flag, which lets users
// attach a reproducible snapshot to bug reports.
type Fixture struct {
	Dir  string
	Meta FixtureMeta
}

type FixtureMeta struct {
	GOARCH   string                 `json:"goarch"`
	UID      int                    `json:"uid"`
	Username string                 `json:"username"`
	GIDs     []int                  `json:"gids"`
	Groups   []string               `json:"groups"`
	Env      map[string]string      `json:"env"`
	Commands []string               `json:"commands"`
	Exec     map[string]FixtureExec `json:"exec"`
	Devices  map[string]FixtureDev  `json:"devices"`
	KVMProbe KVMProbe               `json:"kvm_probe"`
	HTTP     map[string]string      `json:"http"`
}

type FixtureExec struct {
	Out  string `json:"out"`
	Fail bool   `json:"fail"`
}

type FixtureDev struct {
	Mode  string `json:"mode"` // octal, e.g. "0660"
	Major uint32 `json:"major"`
	Minor uint32 `json:"minor"`
	UID   uint32 `json:"uid"`
	GID   uint32 `json:"gid"`
	Group string `json:"group"`
}

func LoadFixture(dir string) (*Fixture, error) {
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	f := &Fixture{Dir: dir}
	if err := json.Unmarshal(b, &f.Meta); err != nil {
		return nil, err
	}
	if f.Meta.GOARCH == "" {
		f.Meta.GOARCH = "amd64"
	}
	return f, nil
}

func (f *Fixture) path(p string) string { return filepath.Join(f.Dir, "root", p) }

func (f *Fixture) ReadFile(p string) ([]byte, error) { return os.ReadFile(f.path(p)) }

func (f *Fixture) ReadDir(p string) ([]string, error) {
	es, err := os.ReadDir(f.path(p))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names, nil
}

func (f *Fixture) Stat(p string) FileStat {
	if d, ok := f.Meta.Devices[p]; ok {
		m, _ := strconv.ParseUint(strings.TrimPrefix(d.Mode, "0"), 8, 32)
		return FileStat{Exists: true, Mode: fs.FileMode(m) | fs.ModeDevice | fs.ModeCharDevice, IsChar: true,
			Major: d.Major, Minor: d.Minor, UID: d.UID, GID: d.GID, Group: d.Group}
	}
	fi, err := os.Stat(f.path(p))
	if err != nil {
		return FileStat{}
	}
	return FileStat{Exists: true, Mode: fi.Mode()}
}

func (f *Fixture) Exec(name string, args ...string) (string, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	r, ok := f.Meta.Exec[key]
	if !ok {
		return "", errors.New("fixture: no such command: " + key)
	}
	if r.Fail {
		return r.Out, errors.New("exit status 1")
	}
	return r.Out, nil
}

func (f *Fixture) LookPathAll(name string) []string {
	var out []string
	for _, c := range f.Meta.Commands {
		if filepath.Base(c) == name {
			out = append(out, c)
		}
	}
	return out
}

func (f *Fixture) Getenv(k string) string { return f.Meta.Env[k] }
func (f *Fixture) UID() int               { return f.Meta.UID }
func (f *Fixture) TargetUser() string     { return f.Meta.Username }
func (f *Fixture) ProcessGIDs() []int     { return f.Meta.GIDs }
func (f *Fixture) MemberGroups() []string { return f.Meta.Groups }
func (f *Fixture) GOARCH() string         { return f.Meta.GOARCH }

func (f *Fixture) ProbeKVM(string) KVMProbe { return f.Meta.KVMProbe }

func (f *Fixture) HTTPGet(method, raw string, _ map[string]string) (string, error) {
	if v, ok := f.Meta.HTTP[method+" "+raw]; ok {
		return v, nil
	}
	return "", errors.New("fixture: no http response")
}
