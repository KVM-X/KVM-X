package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/KVM-X/KVM-X/internal/detect"
	"github.com/KVM-X/KVM-X/internal/guest"
	"github.com/KVM-X/KVM-X/internal/guide"
	"github.com/KVM-X/KVM-X/internal/journal"
	"github.com/KVM-X/KVM-X/internal/ops"
	"github.com/KVM-X/KVM-X/internal/plan"
)

type stepJSON struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Commands []string `json:"changes"`
}

type enableResult struct {
	Before  *detect.Report `json:"before"`
	Steps   []stepJSON     `json:"plan"`
	Manual  []guide.Action `json:"manual_steps"`
	Notes   []string       `json:"notes"`
	Expect  detect.Mode    `json:"expected_mode_after"`
	Applied bool           `json:"applied"`
	Failed  int            `json:"failed_steps"`
	After   *detect.Report `json:"after,omitempty"`
}

func (a *app) cmdEnable(args []string) int {
	var c common
	var o plan.Options
	fs := a.flagSet("enable", &c)
	fs.BoolVar(&o.NoQEMU, "no-qemu", false, "do not install QEMU")
	fs.BoolVar(&o.UML, "uml", false, "also install user-mode Linux (Debian only): Linux guests at native CPU speed without KVM or emulation")
	fs.BoolVar(&o.NoNested, "no-nested", false, "do not set nested=1 for the KVM module")
	fs.StringVar(&o.Shims, "shims", "auto", "install qemu/kvm/virsh compatibility shims: auto (only when stuck on TCG), yes, no")
	fs.BoolVar(&o.Persist, "persist", false, "install a systemd unit (and tmpfiles entry) so the setup survives reboots")
	fs.BoolVar(&o.Boot, "boot", false, "boot-time subset: modules, device node, permissions only; no packages or shims")
	fs.StringVar(&o.GuestArch, "arch", "", "guest architecture whose QEMU to install (default: host architecture)")
	fs.StringVar(&o.ShimDir, "shim-dir", "", "where to put shims (default /usr/local/bin, or ~/.local/bin without root/sudo)")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	if o.Shims != "auto" && o.Shims != "yes" && o.Shims != "no" {
		return a.fail("--shims must be auto, yes or no")
	}
	if c.fixture != "" {
		c.dry = true // never touch the real system on behalf of a fixture
	}
	s, r, err := a.collect(&c, true)
	if err != nil {
		return a.fail("%v", err)
	}
	o.Binary = selfPath()
	p := plan.Build(s, r, o)
	sudo := r.Sandbox.UID != 0
	res := enableResult{Before: r, Manual: p.Manual, Notes: p.Notes, Expect: p.Expect, Steps: []stepJSON{}}
	for _, st := range p.Steps {
		sj := stepJSON{ID: st.ID, Title: st.Title}
		for _, op := range st.Ops {
			sj.Commands = append(sj.Commands, op.Describe(sudo && op.NeedsRoot()))
		}
		res.Steps = append(res.Steps, sj)
	}

	if !c.json && !o.Boot {
		fmt.Fprintf(a.out, "Current status: %s\n", a.modeStyled(r.Mode))
		if r.Potential != r.Mode {
			fmt.Fprintf(a.out, "Reachable:      %s\n", string(r.Potential))
		}
		fmt.Fprintln(a.out)
		p.Print(a.out, sudo)
		if sudo && p.NeedsRoot() {
			fmt.Fprintln(a.out, "\nRoot is needed for the commands above; kvmx will run only those through sudo.")
		}
		a.printManual(p.Manual)
	}

	if len(p.Steps) == 0 || c.dry {
		if c.json {
			a.emit(res)
		} else if c.dry && len(p.Steps) > 0 {
			fmt.Fprintln(a.out, "\n--dry-run: nothing was changed.")
		} else if !o.Boot {
			a.printFinal(r)
		}
		return exitOK
	}
	if !o.Boot && !c.json && !a.confirm("\nApply these changes?", &c) {
		fmt.Fprintln(a.out, "Aborted; nothing was changed.")
		return exitFail
	}

	j := &journal.Journal{Dir: journal.StateDir()}
	w := a.out
	if c.json {
		w = a.err
	}
	failed := plan.Execute(p, &ops.Env{Runner: ops.ExecRunner{}}, j, w)
	res.Applied, res.Failed = true, failed

	_, after, _ := a.collect(&c, true)
	res.After = after
	if c.json {
		a.emit(res)
	} else if !o.Boot {
		fmt.Fprintf(a.out, "\nChanges are logged in %s/journal.jsonl; undo with `kvmx revert`.\n", j.Dir)
		a.printFinal(after)
	}
	if failed > 0 {
		return exitFail
	}
	return exitOK
}

func (a *app) printManual(m []guide.Action) {
	if len(m) == 0 {
		return
	}
	fmt.Fprintln(a.out, "\nManual steps (kvmx cannot do these from inside this machine):")
	for _, x := range m {
		fmt.Fprintf(a.out, "\n  * %s\n", x.Title)
		for _, s := range x.Steps {
			fmt.Fprintf(a.out, "      %s\n", s)
		}
		if x.URL != "" {
			fmt.Fprintf(a.out, "      docs: %s\n", x.URL)
		}
	}
}

func (a *app) printFinal(r *detect.Report) {
	fmt.Fprintf(a.out, "\nFinal status: %s\n", a.modeStyled(r.Mode))
	fmt.Fprintf(a.out, "Expect: %s\n", r.Penalty)
	switch {
	case !r.Ready:
		fmt.Fprintf(a.out, "Not ready: %s\n", r.ReadyNote)
	case r.Mode == detect.ModeTCG:
		fmt.Fprintln(a.out, "This is software emulation, not hardware virtualization. `kvmx run` will use tuned TCG.")
	}
}

// ---- revert / uninstall ----

func (a *app) cmdRevert(args []string) int {
	var c common
	var id int
	var list, rmPkgs bool
	fs := a.flagSet("revert", &c)
	fs.IntVar(&id, "id", 0, "revert only the journal entry with this id")
	fs.BoolVar(&list, "list", false, "list recorded changes and exit")
	fs.BoolVar(&rmPkgs, "remove-packages", false, "also remove packages kvmx installed")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	return a.revert(&c, id, list, rmPkgs)
}

func (a *app) revert(c *common, id int, list, rmPkgs bool) int {
	j := &journal.Journal{Dir: journal.StateDir()}
	entries, err := j.Load()
	if err != nil {
		return a.fail("reading journal: %v", err)
	}
	if list || c.json && c.dry {
		if c.json {
			a.emit(entries)
			return exitOK
		}
		if len(entries) == 0 {
			fmt.Fprintln(a.out, "No changes recorded.")
		}
		for _, e := range entries {
			fmt.Fprintf(a.out, "#%d  %s  [%s]  %s\n", e.ID, e.Time.Format("2006-01-02 15:04:05Z"), e.Step, e.Desc)
		}
		return exitOK
	}
	var todo []journal.Entry
	for _, e := range entries {
		if id == 0 || e.ID == id {
			todo = append(todo, e)
		}
	}
	if len(todo) == 0 {
		fmt.Fprintln(a.out, "Nothing to revert.")
		return exitOK
	}
	fmt.Fprintf(a.out, "Will undo %d change(s), newest first:\n", len(todo))
	for i := len(todo) - 1; i >= 0; i-- {
		e := todo[i]
		fmt.Fprintf(a.out, "  #%d [%s] %s  -> %s\n", e.ID, e.Step, e.Desc, undoText(e.Undo))
	}
	if c.dry {
		fmt.Fprintln(a.out, "--dry-run: nothing was changed.")
		return exitOK
	}
	if !a.confirm("Revert?", c) {
		return exitFail
	}
	env := &ops.Env{Runner: ops.ExecRunner{}}
	done := map[int]bool{}
	failed := 0
	for i := len(todo) - 1; i >= 0; i-- {
		e := todo[i]
		skipped, err := e.Undo.Do(env, rmPkgs)
		switch {
		case err != nil:
			fmt.Fprintf(a.err, "  #%d FAILED: %v (entry kept)\n", e.ID, err)
			failed++
		case skipped != "":
			fmt.Fprintf(a.out, "  #%d note: %s\n", e.ID, skipped)
			if e.Undo.Kind != "pkg-remove" { // kept packages stay journaled so a later --remove-packages can finish
				done[e.ID] = true
			}
		default:
			fmt.Fprintf(a.out, "  #%d reverted\n", e.ID)
			done[e.ID] = true
		}
	}
	var rest []journal.Entry
	for _, e := range entries {
		if !done[e.ID] {
			rest = append(rest, e)
		}
	}
	if err := j.Save(rest); err != nil {
		return a.fail("updating journal: %v", err)
	}
	if failed > 0 {
		return exitFail
	}
	return exitOK
}

func undoText(u ops.Undo) string {
	switch u.Kind {
	case "restore-file":
		return "restore previous contents of " + u.Path
	case "remove-file":
		return "remove " + u.Path
	case "run", "pkg-remove":
		return "$ " + strings.Join(u.Argv, " ")
	}
	if u.Note != "" {
		return u.Note
	}
	return "nothing"
}

func (a *app) cmdUninstall(args []string) int {
	var c common
	var purge, self, rmPkgs bool
	fs := a.flagSet("uninstall", &c)
	fs.BoolVar(&purge, "purge", false, "also delete cached test-guest downloads")
	fs.BoolVar(&self, "self", false, "also delete the kvmx binary")
	fs.BoolVar(&rmPkgs, "remove-packages", false, "also remove packages kvmx installed")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	if code := a.revert(&c, 0, false, rmPkgs); code != exitOK {
		return code
	}
	if c.dry {
		return exitOK
	}
	if purge {
		dir := guest.CacheDir()
		if err := os.RemoveAll(dir); err != nil {
			return a.fail("removing %s: %v", dir, err)
		}
		fmt.Fprintf(a.out, "Removed cache %s\n", dir)
	}
	_ = os.Remove(journal.StateDir()) // only succeeds when empty
	bin := selfPath()
	if self && bin != "" {
		if _, err := (ops.ExecRunner{}).Run([]string{"rm", "-f", bin}, !writable(bin)); err != nil {
			return a.fail("removing %s: %v", bin, err)
		}
		fmt.Fprintf(a.out, "Removed %s\n", bin)
	} else if bin != "" {
		fmt.Fprintf(a.out, "kvmx itself is still at %s (remove it with `kvmx uninstall --self` or `sudo rm %s`).\n", bin, bin)
	}
	return exitOK
}

func writable(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func (a *app) cmdUnit(args []string) int {
	var c common
	fs := a.flagSet("systemd-unit", &c)
	bin := fs.String("binary", "/usr/local/bin/kvmx", "path of the kvmx binary")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	fmt.Fprint(a.out, plan.Unit(*bin))
	return exitOK
}
