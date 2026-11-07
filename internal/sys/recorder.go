package sys

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Recorder wraps a System and remembers every answer, so the exact inputs of a
// detection run can be saved as a Fixture (`kvmx collect`).
type Recorder struct {
	Inner System

	files    map[string][]byte
	execs    map[string]FixtureExec
	devices  map[string]FixtureDev
	commands map[string]bool
	probe    KVMProbe
	http     map[string]string
}

func NewRecorder(inner System) *Recorder {
	return &Recorder{Inner: inner, files: map[string][]byte{}, execs: map[string]FixtureExec{},
		devices: map[string]FixtureDev{}, commands: map[string]bool{}, http: map[string]string{}}
}

func (r *Recorder) ReadFile(p string) ([]byte, error) {
	b, err := r.Inner.ReadFile(p)
	if err == nil {
		r.files[p] = b
	}
	return b, err
}

func (r *Recorder) ReadDir(p string) ([]string, error) { return r.Inner.ReadDir(p) }

func (r *Recorder) Stat(p string) FileStat {
	st := r.Inner.Stat(p)
	switch {
	case st.IsChar:
		r.devices[p] = FixtureDev{Mode: fmt.Sprintf("0%o", st.Mode.Perm()), Major: st.Major, Minor: st.Minor, UID: st.UID, GID: st.GID, Group: st.Group}
	case st.Exists && !st.Mode.IsDir():
		if _, ok := r.files[p]; !ok {
			r.files[p] = nil // existence marker, content unknown
		}
	case st.Exists:
		r.files[strings.TrimRight(p, "/")+"/.keep"] = nil
	}
	return st
}

func (r *Recorder) Exec(name string, args ...string) (string, error) {
	out, err := r.Inner.Exec(name, args...)
	r.execs[strings.Join(append([]string{name}, args...), " ")] = FixtureExec{Out: out, Fail: err != nil}
	return out, err
}

func (r *Recorder) LookPathAll(name string) []string {
	ps := r.Inner.LookPathAll(name)
	for _, p := range ps {
		r.commands[p] = true
	}
	return ps
}

func (r *Recorder) Getenv(k string) string     { return r.Inner.Getenv(k) }
func (r *Recorder) UID() int                   { return r.Inner.UID() }
func (r *Recorder) TargetUser() string         { return r.Inner.TargetUser() }
func (r *Recorder) ProcessGIDs() []int         { return r.Inner.ProcessGIDs() }
func (r *Recorder) MemberGroups() []string     { return r.Inner.MemberGroups() }
func (r *Recorder) GOARCH() string             { return r.Inner.GOARCH() }
func (r *Recorder) ProbeKVM(p string) KVMProbe { r.probe = r.Inner.ProbeKVM(p); return r.probe }

func (r *Recorder) HTTPGet(method, raw string, h map[string]string) (string, error) {
	out, err := r.Inner.HTTPGet(method, raw, h)
	if err == nil {
		r.http[method+" "+raw] = out
	}
	return out, err
}

// Save writes the fixture. The user name is anonymised; module dependency
// files are cut down to their KVM lines.
func (r *Recorder) Save(dir string) error {
	root := filepath.Join(dir, "root")
	for p, b := range r.files {
		if strings.HasSuffix(p, "/modules.dep") || strings.HasSuffix(p, "/modules.builtin") {
			var keep []string
			for _, l := range strings.Split(string(b), "\n") {
				if strings.Contains(l, "kvm") {
					keep = append(keep, l)
				}
			}
			b = []byte(strings.Join(keep, "\n") + "\n")
		}
		dst := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	cmds := make([]string, 0, len(r.commands))
	for c := range r.commands {
		cmds = append(cmds, c)
	}
	sort.Strings(cmds)
	meta := FixtureMeta{GOARCH: r.Inner.GOARCH(), UID: r.Inner.UID(), Username: "user", GIDs: r.Inner.ProcessGIDs(),
		Groups: r.Inner.MemberGroups(), Env: map[string]string{}, Commands: cmds, Exec: r.execs, Devices: r.devices, KVMProbe: r.probe, HTTP: r.http}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), append(b, '\n'), 0o644)
}
