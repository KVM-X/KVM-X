// Package journal records every change kvmx makes so `kvmx revert` can undo it.
package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/KVM-X/KVM-X/internal/ops"
)

type Entry struct {
	ID   int       `json:"id"`
	Time time.Time `json:"time"`
	Step string    `json:"step"`
	Desc string    `json:"desc"`
	Undo ops.Undo  `json:"undo"`
}

type Journal struct{ Dir string }

// StateDir picks where the journal lives: $KVMX_STATE_DIR, else the invoking
// user's ~/.local/state/kvmx (also under sudo), else /var/lib/kvmx for a pure root login.
func StateDir() string {
	if d := os.Getenv("KVMX_STATE_DIR"); d != "" {
		return d
	}
	if su := os.Getenv("SUDO_USER"); su != "" && os.Geteuid() == 0 {
		if u, err := user.Lookup(su); err == nil {
			return filepath.Join(u.HomeDir, ".local", "state", "kvmx")
		}
	}
	if os.Geteuid() == 0 {
		return "/var/lib/kvmx"
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "kvmx")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "kvmx")
	}
	return filepath.Join(os.TempDir(), "kvmx-state")
}

func (j *Journal) path() string { return filepath.Join(j.Dir, "journal.jsonl") }

func (j *Journal) Load() ([]Entry, error) {
	b, err := os.ReadFile(j.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Append records an entry, assigning the next ID.
func (j *Journal) Append(e Entry) (Entry, error) {
	all, err := j.Load()
	if err != nil {
		return e, err
	}
	e.ID = 1
	if n := len(all); n > 0 {
		e.ID = all[n-1].ID + 1
	}
	e.Time = time.Now().UTC().Truncate(time.Second)
	if err := os.MkdirAll(j.Dir, 0o755); err != nil {
		return e, err
	}
	f, err := os.OpenFile(j.path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return e, err
	}
	defer f.Close()
	chownToSudoUser(j.Dir, j.path())
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return e, err
}

// chownToSudoUser hands files created by `sudo kvmx ...` back to the invoking
// user, so a later unprivileged `kvmx revert` can still rewrite the journal.
func chownToSudoUser(paths ...string) {
	su := os.Getenv("SUDO_USER")
	if su == "" || os.Geteuid() != 0 {
		return
	}
	u, err := user.Lookup(su)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	for _, p := range paths {
		_ = os.Chown(p, uid, gid)
	}
}

// Save replaces the journal with entries (used after a partial revert).
func (j *Journal) Save(entries []Entry) error {
	if len(entries) == 0 {
		err := os.Remove(j.path())
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var buf bytes.Buffer
	for _, e := range entries {
		b, _ := json.Marshal(e)
		buf.Write(append(b, '\n'))
	}
	return os.WriteFile(j.path(), buf.Bytes(), 0o644)
}
