package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KVM-X/KVM-X/internal/detect"
	"github.com/KVM-X/KVM-X/internal/guest"
	"github.com/KVM-X/KVM-X/internal/qemu"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func parseMem(v string) (int, error) {
	v = strings.ToUpper(strings.TrimSpace(v))
	mult := 1
	switch {
	case strings.HasSuffix(v, "G"):
		mult, v = 1024, strings.TrimSuffix(v, "G")
	case strings.HasSuffix(v, "M"):
		v = strings.TrimSuffix(v, "M")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("bad memory size %q (use 512, 512M or 2G)", v)
	}
	return n * mult, nil
}

type runResult struct {
	Mode       string   `json:"mode"`
	Profile    string   `json:"tcg_profile,omitempty"`
	Command    []string `json:"command"`
	ExitCode   int      `json:"exit_code"`
	ElapsedMS  int64    `json:"elapsed_ms"`
	Expect     string   `json:"expect,omitempty"`
	Matched    bool     `json:"expect_matched,omitempty"`
	ExpectMS   int64    `json:"expect_ms,omitempty"`
	TimedOut   bool     `json:"timed_out,omitempty"`
	GuestBench []string `json:"guest_bench,omitempty"`
}

func (a *app) cmdRun(args []string) int {
	var c common
	var (
		arch, mode, profile, disk, cdrom, kernel, initrd, appendArgs, machine, display = "", "auto", qemu.DefaultProfile, "", "", "", "", "", "", "none"
		cpus                                                                           int
		mem                                                                            = "1G"
		noNet, demo, bench, stopOnExpect                                               bool
		expect                                                                         string
		timeout                                                                        time.Duration
		tbSize                                                                         int
		thread, shareMode                                                              string
		hostfwd, shares                                                                stringList
		backend, rootfs, initPath                                                      string
	)
	fs := a.flagSet("run", &c)
	fs.StringVar(&arch, "arch", "", "guest architecture: x86_64 or aarch64 (default: this machine's)")
	fs.StringVar(&mode, "mode", "auto", "auto (KVM if usable, else TCG) | kvm (never fall back) | tcg (force emulation)")
	fs.StringVar(&profile, "tcg-profile", qemu.DefaultProfile, "TCG tuning profile: compat (fastest per vCPU, old ISA) | balanced (default: modern ISA, multi-threaded)")
	fs.IntVar(&tbSize, "tb-size", 0, "override TCG translation-block cache size in MiB")
	fs.StringVar(&thread, "tcg-thread", "", "override TCG threading: multi | single")
	fs.IntVar(&cpus, "cpus", 0, "vCPUs (default 2; TCG profiles cap it where more CPUs slow things down)")
	fs.StringVar(&mem, "mem", "1G", "guest memory, e.g. 512M, 2G")
	fs.StringVar(&disk, "disk", "", "disk image (raw or qcow2)")
	fs.StringVar(&cdrom, "cdrom", "", "ISO to attach")
	fs.StringVar(&kernel, "kernel", "", "direct-boot kernel")
	fs.StringVar(&initrd, "initrd", "", "initramfs for --kernel")
	fs.StringVar(&appendArgs, "append", "", "kernel command line")
	fs.StringVar(&machine, "machine", "", "QEMU machine type override (pc, q35, microvm, virt)")
	fs.BoolVar(&noNet, "no-net", false, "no network device")
	fs.Var(&hostfwd, "hostfwd", "forward a host port to the guest, e.g. tcp::2222-:22 (repeatable)")
	fs.Var(&shares, "share", "share a host directory: DIR or DIR:TAG (repeatable)")
	fs.StringVar(&shareMode, "share-mode", "9p", "9p | virtiofs (needs virtiofsd)")
	fs.StringVar(&display, "display", "none", "none (serial console on this terminal) | vnc=:0 | gtk | sdl")
	fs.StringVar(&backend, "backend", "qemu", "qemu (KVM or TCG) | uml (user-mode Linux: Linux guests only, not a VM, no KVM or emulation needed)")
	fs.StringVar(&rootfs, "rootfs", "", "uml backend: host directory to use as the guest root filesystem")
	fs.StringVar(&initPath, "init", "", "uml backend: guest init program (default /sbin/init, or the demo script)")
	fs.BoolVar(&demo, "demo", false, "boot the tiny built-in Alpine test guest (downloads ~22 MB once, sha256-pinned)")
	fs.BoolVar(&bench, "guest-bench", false, "with --demo: run the in-guest CPU benchmark")
	fs.StringVar(&expect, "expect", "", "stop successfully when the guest prints this string (for tests)")
	fs.BoolVar(&stopOnExpect, "stop-on-expect", false, "kill the VM as soon as --expect is seen")
	fs.DurationVar(&timeout, "timeout", 0, "kill the VM after this long (0 = no limit)")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	extra := fs.Args()
	if c.fixture != "" {
		c.dry = true // a fixture describes another machine: only ever print the command
	}
	switch backend {
	case "qemu":
	case "uml":
		return a.runUML(&c, umlOpts{mem: mem, demo: demo, bench: bench, rootfs: rootfs, init: initPath, expect: expect, stop: stopOnExpect, timeout: timeout, extra: extra})
	default:
		return a.fail("--backend must be qemu or uml")
	}

	s, err := a.system(&c)
	if err != nil {
		return a.fail("%v", err)
	}
	r := detect.Collect(s, detect.Options{Version: Version})
	hostArch := r.CPU.Arch
	if arch == "" {
		arch = hostArch
	}
	prof, ok := qemu.Profiles[profile]
	if !ok {
		return a.fail("unknown --tcg-profile %q (compat, balanced)", profile)
	}
	if tbSize > 0 {
		prof.TBSizeMB = tbSize
	}
	if thread != "" {
		prof.Thread = thread
	}
	memMB, err := parseMem(mem)
	if err != nil {
		return a.fail("%v", err)
	}

	// Resolve the mode honestly: never present TCG as KVM, never fall back silently when --mode kvm.
	m := "tcg"
	switch mode {
	case "auto":
		if r.KVM.Usable && arch == hostArch {
			m = "kvm"
		}
	case "kvm":
		if !r.KVM.Usable || arch != hostArch {
			why := "KVM is not usable here"
			if arch != hostArch {
				why = "cross-architecture guests cannot use KVM"
			}
			fmt.Fprintf(a.err, "kvmx: --mode kvm requested but %s (status: %s).\n", why, r.Mode)
			for _, b := range r.Blockers {
				fmt.Fprintf(a.err, "  - %s\n", b.Summary)
			}
			fmt.Fprintln(a.err, "Run `kvmx doctor`, or use --mode auto to accept software emulation.")
			return exitFail
		}
		m = "kvm"
	case "tcg":
	default:
		return a.fail("--mode must be auto, kvm or tcg")
	}
	label := detect.ModeTCG
	if m == "kvm" {
		label = r.Mode
	}

	sp := qemu.Spec{Arch: arch, Mode: m, Profile: prof, CPUs: cpus, MemMB: memMB, Machine: machine, Disk: disk, CDROM: cdrom,
		Kernel: kernel, Initrd: initrd, Append: appendArgs, NoNet: noNet, HostFwd: hostfwd, Display: display, Extra: extra,
		Firmware: qemu.FindFirmware(arch)}
	for i, sh := range shares {
		dir, tag, _ := strings.Cut(sh, ":")
		if tag == "" {
			tag = fmt.Sprintf("share%d", i)
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return a.fail("%v", err)
		}
		sp.Shares = append(sp.Shares, qemu.Share{Dir: abs, Tag: tag, Mode: shareMode, Socket: filepath.Join(os.TempDir(), fmt.Sprintf("kvmx-vfs-%d-%d.sock", os.Getpid(), i))})
	}

	if demo {
		if c.dry || c.fixture != "" {
			sp.Kernel, sp.Initrd = "<alpine-vmlinuz-virt>", "<alpine-initramfs+init>"
		} else {
			p, err := guest.Ensure(arch, guest.EnsureOpts{Yes: c.yes, TTY: a.tty, Out: a.err, In: a.in})
			if err != nil {
				return a.fail("%v", err)
			}
			img, err := guest.Overlay(p.Initrd, guest.InitScript, filepath.Join(guest.CacheDir(), "guest", "overlay"))
			if err != nil {
				return a.fail("%v", err)
			}
			sp.Kernel, sp.Initrd = p.Kernel, img
		}
		if bench {
			sp.Append = strings.TrimSpace(sp.Append + " console=" + map[string]string{"x86_64": "ttyS0", "aarch64": "ttyAMA0"}[arch] + " kvmx.bench=1")
		}
		if expect == "" {
			expect, stopOnExpect = "KVMX-DONE", false
		}
		noNet = true
		sp.NoNet = true
	}

	argv, err := sp.Args()
	if err != nil {
		return a.fail("%v", err)
	}
	bin := qemu.Binary(arch)
	if !c.dry {
		if bin, err = qemu.Find(s, arch); err != nil {
			return a.fail("%v", err)
		}
	}
	full := append([]string{bin}, argv...)

	banner := fmt.Sprintf("kvmx: mode %s - %s", a.modeStyled(label), label.Penalty())
	if m == "tcg" {
		banner += fmt.Sprintf("\nkvmx: tcg profile %s (thread=%s tb-size=%dMiB cpu=%s)", prof.Name, orDefaultS(prof.Thread, "multi"), prof.TBSizeMB, prof.CPU)
	}
	if !c.json {
		fmt.Fprintln(a.err, banner)
	}
	if c.dry {
		if c.json {
			a.emit(runResult{Mode: string(label), Profile: prof.Name, Command: full})
		} else {
			fmt.Fprintln(a.out, shellJoin(full))
		}
		return exitOK
	}

	// virtiofs shares need a running virtiofsd per share.
	for _, sh := range sp.Shares {
		if sh.Mode != "virtiofs" {
			continue
		}
		stop, err := startVirtiofsd(r.Tools.Virtiofsd, sh)
		if err != nil {
			return a.fail("%v", err)
		}
		defer stop()
	}

	interactive := expect == "" && a.tty
	po := procOpts{Timeout: timeout, Expect: expect, StopOnExpect: stopOnExpect}
	if interactive {
		po.Stdin, po.Direct = os.Stdin, true
	}
	if !c.json || interactive {
		po.Out = a.out
	}
	res := runProcess(bin, argv, po)

	rr := runResult{Mode: string(label), Profile: prof.Name, Command: full, ExitCode: res.ExitCode, ElapsedMS: res.Elapsed.Milliseconds(),
		Expect: expect, Matched: res.Matched, ExpectMS: res.MatchAt.Milliseconds(), TimedOut: res.TimedOut}
	for _, l := range strings.Split(res.Output, "\n") {
		if strings.Contains(l, "KVMX-BENCH") {
			rr.GuestBench = append(rr.GuestBench, strings.TrimSpace(l))
		}
	}
	if c.json {
		a.emit(rr)
	} else if expect != "" {
		fmt.Fprintf(a.err, "\nkvmx: %s: expect %q %s after %.1fs (VM ran %.1fs)\n", label, expect, map[bool]string{true: "seen", false: "NOT seen"}[res.Matched],
			res.MatchAt.Seconds(), res.Elapsed.Seconds())
	}
	switch {
	case expect != "":
		if res.Matched {
			return exitOK
		}
		return exitFail
	case res.TimedOut:
		return exitFail
	}
	if res.ExitCode < 0 {
		return exitFail
	}
	return res.ExitCode
}

func orDefaultS(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func shellJoin(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if a == "" || strings.ContainsAny(a, " \t\"'$`\\;&|<>()*?[]{}!#~") {
			b.WriteString("'" + strings.ReplaceAll(a, "'", `'\''`) + "'")
		} else {
			b.WriteString(a)
		}
	}
	return b.String()
}

// ---- process runner ----

type procOpts struct {
	Timeout      time.Duration
	Marks        []string // extra markers whose first-seen time is recorded
	Expect       string
	StopOnExpect bool
	Stdin        *os.File
	Env          []string
	Out          interface{ Write([]byte) (int, error) }
	Direct       bool // attach the real terminal (interactive console)
}

type procResult struct {
	ExitCode int
	Elapsed  time.Duration
	Matched  bool
	MatchAt  time.Duration
	MarkAt   map[string]time.Duration
	TimedOut bool
	Output   string
}

type scanWriter struct {
	mu      sync.Mutex
	start   time.Time
	expect  []byte
	buf     bytes.Buffer
	out     interface{ Write([]byte) (int, error) }
	matched bool
	at      time.Duration
	marks   map[string]time.Duration
	onMatch func()
}

func (w *scanWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.out != nil {
		_, _ = w.out.Write(p)
	}
	if w.buf.Len() < 4<<20 {
		w.buf.Write(p)
	}
	for m, t := range w.marks {
		if t < 0 && bytes.Contains(w.buf.Bytes(), []byte(m)) {
			w.marks[m] = time.Since(w.start)
		}
	}
	if len(w.expect) > 0 && !w.matched && bytes.Contains(w.buf.Bytes(), w.expect) {
		w.matched, w.at = true, time.Since(w.start)
		if w.onMatch != nil {
			go w.onMatch()
		}
	}
	return len(p), nil
}

func runProcess(bin string, args []string, o procOpts) procResult {
	ctx := context.Background()
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // bin and args come from kvmx's own QEMU builder
	// SIGTERM first so QEMU restores the terminal; SIGKILL if it hangs.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	sw := &scanWriter{expect: []byte(o.Expect), out: o.Out, marks: map[string]time.Duration{}}
	for _, m := range o.Marks {
		sw.marks[m] = -1
	}
	if o.Stdin != nil {
		cmd.Stdin = o.Stdin
	}
	if o.Env != nil {
		cmd.Env = o.Env
	}
	if o.Direct {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	} else {
		cmd.Stdout, cmd.Stderr = sw, sw
	}
	if o.StopOnExpect {
		sw.onMatch = func() {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
		}
	}
	sw.start = time.Now()
	err := cmd.Start()
	if err != nil {
		return procResult{ExitCode: 127, Output: err.Error()}
	}
	sw.start = time.Now()
	_ = cmd.Wait()
	res := procResult{Elapsed: time.Since(sw.start), TimedOut: ctx.Err() == context.DeadlineExceeded}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	sw.mu.Lock()
	res.Matched, res.MatchAt, res.Output = sw.matched, sw.at, sw.buf.String()
	res.MarkAt = map[string]time.Duration{}
	for m, t := range sw.marks {
		if t >= 0 {
			res.MarkAt[m] = t
		}
	}
	sw.mu.Unlock()
	return res
}

func startVirtiofsd(bin string, sh qemu.Share) (func(), error) {
	if bin == "" {
		return nil, fmt.Errorf("--share-mode virtiofs needs virtiofsd, which is not installed (try --share-mode 9p)")
	}
	_ = os.Remove(sh.Socket)
	cmd := exec.Command(bin, "--socket-path="+sh.Socket, "--shared-dir="+sh.Dir, "--sandbox", "none") //nolint:gosec
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sh.Socket); err == nil {
			return func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = os.Remove(sh.Socket) }, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return nil, fmt.Errorf("virtiofsd did not create %s", sh.Socket)
}
