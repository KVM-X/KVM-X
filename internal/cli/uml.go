package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KVM-X/KVM-X/internal/guest"
	"github.com/KVM-X/KVM-X/internal/uml"
)

// umlLabel is deliberately not one of the three KVM/TCG statuses: UML is not
// a virtual machine and kvmx never presents it as one.
const umlLabel = "USERSPACE-KERNEL (UML)"
const umlNote = "user-mode Linux is not a VM and not hardware virtualization: Linux guests only; guest userland runs at native speed, system calls are slower (ptrace)"

type umlOpts struct {
	mem, rootfs, init, expect string
	demo, bench, stop         bool
	timeout                   time.Duration
	extra                     []string
}

func (a *app) runUML(c *common, o umlOpts) int {
	s, err := a.system(c)
	if err != nil {
		return a.fail("%v", err)
	}
	memMB, err := parseMem(orDefaultS(o.mem, "256M"))
	if err != nil {
		return a.fail("%v", err)
	}
	bin := uml.Find(s)
	if bin == "" && !c.dry {
		return a.fail("user-mode Linux (linux.uml) is not installed. Debian 12/13: apt-get install user-mode-linux (or `kvmx enable --uml`); elsewhere build a kernel with ARCH=um")
	}
	rootfs, init := o.rootfs, o.init
	switch {
	case o.demo && (c.dry || c.fixture != ""):
		rootfs, init = "<alpine-minirootfs>", "/kvmx-init"
	case o.demo:
		if rootfs, err = guest.Rootfs("x86_64", guest.EnsureOpts{Yes: c.yes, TTY: a.tty, Out: a.err, In: a.in}); err != nil {
			return a.fail("%v", err)
		}
		if err := os.WriteFile(filepath.Join(rootfs, "kvmx-init"), []byte(guest.InitScript), 0o755); err != nil { //nolint:gosec // must be executable inside the guest
			return a.fail("%v", err)
		}
		init = "/kvmx-init"
	case rootfs == "":
		return a.fail("--backend uml needs --rootfs DIR (or --demo)")
	}
	if init == "" {
		init = "/sbin/init"
	}
	extra := append([]string{}, o.extra...)
	if o.bench {
		extra = append(extra, "kvmx.bench=1")
	}
	expect := o.expect
	if o.demo && expect == "" {
		expect = "KVMX-DONE"
	}
	tmp := "/tmp"
	if !c.dry {
		if tmp, err = uml.TmpDir(s); err != nil {
			return a.fail("%v", err)
		}
	}
	argv := uml.Args(rootfs, init, memMB, extra...)
	full := append([]string{orDefaultS(bin, "linux.uml")}, argv...)
	if !c.json {
		fmt.Fprintf(a.err, "kvmx: backend %s - %s\n", a.paint("1;35", umlLabel), umlNote)
	}
	if c.dry {
		if c.json {
			a.emit(runResult{Mode: umlLabel, Command: full})
		} else {
			fmt.Fprintln(a.out, "TMPDIR="+tmp+" "+shellJoin(full))
		}
		return exitOK
	}
	po := procOpts{Timeout: o.timeout, Expect: expect, StopOnExpect: o.stop, Env: uml.Environment(tmp)}
	if expect == "" && a.tty {
		po.Stdin, po.Direct = os.Stdin, true
	}
	if !c.json || po.Direct {
		po.Out = a.out
	}
	res := runProcess(bin, argv, po)
	rr := runResult{Mode: umlLabel, Command: full, ExitCode: res.ExitCode, ElapsedMS: res.Elapsed.Milliseconds(), Expect: expect,
		Matched: res.Matched, ExpectMS: res.MatchAt.Milliseconds(), TimedOut: res.TimedOut}
	for _, l := range strings.Split(res.Output, "\n") {
		if strings.Contains(l, "KVMX-BENCH") {
			rr.GuestBench = append(rr.GuestBench, strings.TrimSpace(l))
		}
	}
	if c.json {
		a.emit(rr)
	} else if expect != "" {
		fmt.Fprintf(a.err, "\nkvmx: %s: expect %q %s after %.1fs\n", umlLabel, expect, map[bool]string{true: "seen", false: "NOT seen"}[res.Matched], res.MatchAt.Seconds())
	}
	switch {
	case expect != "":
		if res.Matched {
			return exitOK
		}
		return exitFail
	case res.TimedOut || res.ExitCode < 0:
		return exitFail
	}
	return res.ExitCode
}
