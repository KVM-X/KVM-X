// Package ops defines the small set of reversible system changes kvmx can make.
// Every Op describes itself exactly, applies idempotently and returns the
// inverse so it can be journaled and reverted.
package ops

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner executes commands, escalating with sudo when asked and not root.
type Runner interface {
	Run(argv []string, priv bool) (string, error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

func (ExecRunner) Run(argv []string, priv bool) (string, error) {
	if priv && os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err != nil {
			return "", errors.New("root is required and sudo is not installed")
		}
		argv = append([]string{"sudo", "--"}, argv...)
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is built by kvmx plan steps
	cmd.Stdin = os.Stdin                      // lets sudo prompt on the tty
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, out)
	}
	return out, nil
}

// Env is where ops act. Root prefixes file paths and exists for tests only.
type Env struct {
	Root   string
	Runner Runner
}

func (e *Env) path(p string) string {
	if e.Root == "" {
		return p
	}
	return filepath.Join(e.Root, p)
}

// direct reports whether file writes can happen in-process (root, or a test root).
func (e *Env) direct() bool { return e.Root != "" || os.Geteuid() == 0 }

// Undo is the journaled inverse of an applied Op.
type Undo struct {
	Kind    string   `json:"kind"` // none | restore-file | remove-file | run | pkg-remove
	Path    string   `json:"path,omitempty"`
	Prev    string   `json:"prev_b64,omitempty"`
	Mode    uint32   `json:"mode,omitempty"`
	Argv    []string `json:"argv,omitempty"`
	Note    string   `json:"note,omitempty"`
	AsUser  bool     `json:"as_user,omitempty"`
	Pkgs    []string `json:"pkgs,omitempty"`
	Applied bool     `json:"-"`
}

var None = Undo{Kind: "none"}

// Op is one system change.
type Op interface {
	// Describe shows exactly what Apply will do. sudo prefixes privileged commands.
	Describe(sudo bool) string
	Apply(e *Env) (Undo, error)
	NeedsRoot() bool
}

func sudoPrefix(sudo bool) string {
	if sudo {
		return "sudo "
	}
	return ""
}

// ---- WriteFile ----

type WriteFile struct {
	Path    string
	Content string
	Mode    fs.FileMode
	User    bool // path is user-owned; never escalate
}

func (o WriteFile) NeedsRoot() bool { return !o.User }

func (o WriteFile) Describe(sudo bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "write %s (mode %04o)", o.Path, o.Mode.Perm())
	for _, l := range strings.Split(strings.TrimRight(o.Content, "\n"), "\n") {
		b.WriteString("\n      | " + l)
	}
	return b.String()
}

func (o WriteFile) Apply(e *Env) (Undo, error) {
	p := e.path(o.Path)
	prev, existed := readIfExists(e, p, o.User)
	if existed && string(prev) == o.Content {
		return None, nil
	}
	if err := e.writeFile(p, o.Path, []byte(o.Content), o.Mode, o.User); err != nil {
		return None, err
	}
	if !existed {
		return Undo{Kind: "remove-file", Path: o.Path, AsUser: o.User}, nil
	}
	return Undo{Kind: "restore-file", Path: o.Path, Prev: base64.StdEncoding.EncodeToString(prev), Mode: uint32(o.Mode), AsUser: o.User}, nil
}

func readIfExists(e *Env, p string, user bool) ([]byte, bool) {
	b, err := os.ReadFile(p)
	if err == nil {
		return b, true
	}
	if errors.Is(err, fs.ErrPermission) && !user && e.Runner != nil {
		if out, err := e.Runner.Run([]string{"cat", p}, true); err == nil {
			return []byte(out + "\n"), true
		}
	}
	return nil, false
}

func (e *Env) writeFile(realPath, shownPath string, content []byte, mode fs.FileMode, user bool) error {
	if e.direct() || user {
		if err := os.MkdirAll(filepath.Dir(realPath), 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(realPath), ".kvmx-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(content); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Chmod(tmp.Name(), mode); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), realPath)
	}
	tmp, err := os.CreateTemp("", "kvmx-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	_ = tmp.Close()
	_, err = e.Runner.Run([]string{"install", "-D", "-m", fmt.Sprintf("%04o", mode.Perm()), tmp.Name(), shownPath}, true)
	return err
}

func (e *Env) remove(realPath, shownPath string, user bool) error {
	if e.direct() || user {
		if err := os.Remove(realPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	_, err := e.Runner.Run([]string{"rm", "-f", shownPath}, true)
	return err
}

// ---- Symlink ----

type Symlink struct {
	Path, Target string
	User         bool
}

func (o Symlink) NeedsRoot() bool { return !o.User }
func (o Symlink) Describe(sudo bool) string {
	return fmt.Sprintf("symlink %s -> %s", o.Path, o.Target)
}

func (o Symlink) Apply(e *Env) (Undo, error) {
	p := e.path(o.Path)
	if t, err := os.Readlink(p); err == nil {
		if t == o.Target {
			return None, nil
		}
		return None, fmt.Errorf("%s already points to %s; refusing to replace it", o.Path, t)
	}
	if _, err := os.Lstat(p); err == nil {
		return None, fmt.Errorf("%s exists and is not a symlink; refusing to replace it", o.Path)
	}
	if e.direct() || o.User {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return None, err
		}
		if err := os.Symlink(o.Target, p); err != nil {
			return None, err
		}
	} else if _, err := e.Runner.Run([]string{"ln", "-s", o.Target, o.Path}, true); err != nil {
		return None, err
	}
	return Undo{Kind: "remove-file", Path: o.Path, AsUser: o.User}, nil
}

// ---- Mknod ----

type Mknod struct {
	Path         string
	Major, Minor int
	Mode         string
	Group        string // optional chgrp
}

func (o Mknod) NeedsRoot() bool { return true }
func (o Mknod) Describe(sudo bool) string {
	s := fmt.Sprintf("$ %smknod -m %s %s c %d %d", sudoPrefix(sudo), o.Mode, o.Path, o.Major, o.Minor)
	if o.Group != "" {
		s += fmt.Sprintf("\n  $ %schgrp %s %s", sudoPrefix(sudo), o.Group, o.Path)
	}
	return s
}

func (o Mknod) Apply(e *Env) (Undo, error) {
	if _, err := os.Stat(e.path(o.Path)); err == nil {
		return None, nil
	}
	if _, err := e.Runner.Run([]string{"mknod", "-m", o.Mode, o.Path, "c", fmt.Sprint(o.Major), fmt.Sprint(o.Minor)}, true); err != nil {
		return None, err
	}
	u := Undo{Kind: "remove-file", Path: o.Path}
	if o.Group != "" {
		if _, err := e.Runner.Run([]string{"chgrp", o.Group, o.Path}, true); err != nil {
			return u, err
		}
	}
	return u, nil
}

// ---- Run ----

// Run executes a command. UndoArgv, if set, is journaled as the inverse.
type Run struct {
	Argv     []string
	UndoArgv []string
	Priv     bool
	Note     string // shown on revert when there is no automatic inverse
	IfExists string // skip silently unless this path exists when applied
}

func (o Run) NeedsRoot() bool { return o.Priv }
func (o Run) Describe(sudo bool) string {
	return "$ " + sudoPrefix(sudo && o.Priv) + strings.Join(o.Argv, " ")
}

func (o Run) Apply(e *Env) (Undo, error) {
	if o.IfExists != "" {
		if _, err := os.Stat(e.path(o.IfExists)); err != nil {
			return None, nil
		}
	}
	if _, err := e.Runner.Run(o.Argv, o.Priv); err != nil {
		return None, err
	}
	if o.UndoArgv != nil {
		return Undo{Kind: "run", Argv: o.UndoArgv}, nil
	}
	if o.Note != "" {
		return Undo{Kind: "none", Note: o.Note}, nil
	}
	return None, nil
}

// ---- PkgInstall ----

// PkgInstall installs packages. Revert removes only packages that were absent
// before, and only when the user passes --remove-packages.
type PkgInstall struct {
	Cmds      [][]string
	Pkgs      []string
	RemoveCmd []string
	Installed func(pkg string) bool
}

func (o PkgInstall) NeedsRoot() bool { return true }
func (o PkgInstall) Describe(sudo bool) string {
	var b strings.Builder
	for i, c := range o.Cmds {
		if i > 0 {
			b.WriteString("\n  ")
		}
		b.WriteString("$ " + sudoPrefix(sudo) + strings.Join(c, " "))
	}
	return b.String()
}

func (o PkgInstall) Apply(e *Env) (Undo, error) {
	var fresh []string
	for _, p := range o.Pkgs {
		if o.Installed == nil || !o.Installed(p) {
			fresh = append(fresh, p)
		}
	}
	if len(fresh) == 0 {
		return None, nil
	}
	for _, c := range o.Cmds {
		if _, err := e.Runner.Run(c, true); err != nil {
			return None, err
		}
	}
	return Undo{Kind: "pkg-remove", Argv: append(append([]string{}, o.RemoveCmd...), fresh...), Pkgs: fresh}, nil
}

// ---- Undo execution ----

// Do performs the inverse. removePkgs gates package removal.
func (u Undo) Do(e *Env, removePkgs bool) (skipped string, err error) {
	switch u.Kind {
	case "none", "":
		return u.Note, nil
	case "restore-file":
		prev, derr := base64.StdEncoding.DecodeString(u.Prev)
		if derr != nil {
			return "", derr
		}
		return "", e.writeFile(e.path(u.Path), u.Path, prev, fs.FileMode(u.Mode), u.AsUser)
	case "remove-file":
		return "", e.remove(e.path(u.Path), u.Path, u.AsUser)
	case "run":
		_, err = e.Runner.Run(u.Argv, true)
		return "", err
	case "pkg-remove":
		if !removePkgs {
			return "packages left installed (pass --remove-packages to remove): " + strings.Join(u.Pkgs, " "), nil
		}
		_, err = e.Runner.Run(u.Argv, true)
		return "", err
	}
	return "", fmt.Errorf("unknown undo kind %q", u.Kind)
}
