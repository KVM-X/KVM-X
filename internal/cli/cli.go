// Package cli implements the kvmx command line.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/KVM-X/KVM-X/internal/detect"
	"github.com/KVM-X/KVM-X/internal/sys"
)

// Set at build time: -ldflags "-X github.com/KVM-X/KVM-X/internal/cli.Version=v0.1.0".
var (
	Version = "dev"
	Commit  = ""
)

// Exit codes. `kvmx status` uses 0/10/20 so scripts can branch on the rung.
const (
	exitOK       = 0
	exitFail     = 1
	exitUsage    = 2
	exitSoftware = 10 // working, but software emulation (TCG)
	exitNotReady = 20 // nothing usable yet (for example QEMU missing)
)

type app struct {
	in    io.Reader
	out   io.Writer
	err   io.Writer
	color bool
	tty   bool
}

// common holds flags shared by every subcommand.
type common struct {
	json, dry, yes, quiet bool
	fixture               string
	metadata              bool
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// Main runs kvmx and returns the process exit code. When invoked through a
// shim symlink (qemu-system-*, kvm, virsh) it behaves as that tool instead.
func Main(args []string, in io.Reader, out, errw io.Writer) int {
	a := &app{in: in, out: out, err: errw}
	if f, ok := out.(*os.File); ok {
		a.tty = isTTY(f)
		a.color = a.tty && os.Getenv("NO_COLOR") == ""
	}
	if code, ok := a.shim(args); ok {
		return code
	}
	if len(args) < 2 {
		a.usage()
		return exitUsage
	}
	cmd, rest := args[1], args[2:]
	switch cmd {
	case "detect":
		return a.cmdDetect(rest)
	case "status":
		return a.cmdStatus(rest)
	case "doctor":
		return a.cmdDoctor(rest)
	case "enable":
		return a.cmdEnable(rest)
	case "revert":
		return a.cmdRevert(rest)
	case "uninstall":
		return a.cmdUninstall(rest)
	case "run":
		return a.cmdRun(rest)
	case "bench":
		return a.cmdBench(rest)
	case "collect":
		return a.cmdCollect(rest)
	case "flags":
		return a.cmdFlags(rest)
	case "libvirt":
		return a.cmdLibvirt(rest)
	case "systemd-unit":
		return a.cmdUnit(rest)
	case "version", "--version", "-version":
		fmt.Fprintf(out, "kvmx %s %s/%s%s\n", Version, runtime.GOOS, runtime.GOARCH, commitSuffix())
		return exitOK
	case "help", "--help", "-h", "-help":
		a.usage()
		return exitOK
	}
	fmt.Fprintf(errw, "kvmx: unknown command %q\n\n", cmd)
	a.usage()
	return exitUsage
}

func commitSuffix() string {
	if Commit == "" {
		return ""
	}
	return " (" + Commit + ")"
}

func (a *app) usage() {
	fmt.Fprint(a.err, `kvmx - get a working VM environment on machines without nested virtualization

Usage: kvmx <command> [flags]

  detect      show what this machine can do (CPU, hypervisor, /dev/kvm, containers, provider)
  status      one-line verdict: HARDWARE-KVM, NESTED-KVM or SOFTWARE-EMULATION (TCG)
  doctor      full checklist with fixes and provider-specific instructions
  enable      climb the ladder: show the plan, then apply reversible changes
  run         boot a VM using the best available mode (KVM, else tuned TCG)
  bench       benchmark TCG tuning options by booting a real guest
  collect     record what detection reads into a fixture directory (for platform reports)
  flags       print Docker/Podman/LXC/LXD/Kubernetes flags that expose /dev/kvm
  libvirt     domain XML for TCG, and rewriting KVM domains to run without KVM
  revert      undo changes recorded by kvmx enable
  uninstall   revert everything and remove kvmx state
  version

Every command accepts --json and --dry-run. Exit codes of 'status':
  0 KVM (hardware or nested)   10 software emulation (TCG)   20 nothing usable yet

Docs: https://github.com/KVM-X/KVM-X
`)
}

func (a *app) flagSet(name string, c *common) *flag.FlagSet {
	fs := flag.NewFlagSet("kvmx "+name, flag.ContinueOnError)
	fs.SetOutput(a.err)
	fs.BoolVar(&c.json, "json", false, "machine-readable JSON output")
	fs.BoolVar(&c.dry, "dry-run", false, "show what would happen, change nothing")
	fs.BoolVar(&c.yes, "yes", false, "do not ask for confirmation")
	fs.BoolVar(&c.yes, "y", false, "shorthand for --yes")
	fs.BoolVar(&c.quiet, "quiet", false, "less output")
	fs.BoolVar(&c.quiet, "q", false, "shorthand for --quiet")
	fs.BoolVar(&c.metadata, "cloud-metadata", false, "also query the link-local cloud metadata service (169.254.169.254) for the instance type")
	fs.StringVar(&c.fixture, "fixture", "", "inspect a recorded fixture directory instead of this machine (for bug reports and tests)")
	return fs
}

// parse returns false (and the exit code) when flag parsing already reported a problem.
func parse(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return exitOK, false
		}
		return exitUsage, false
	}
	return 0, true
}

func (a *app) system(c *common) (sys.System, error) {
	if c.fixture != "" {
		return sys.LoadFixture(c.fixture)
	}
	return sys.Host{}, nil
}

func (a *app) collect(c *common, probeSudo bool) (sys.System, *detect.Report, error) {
	s, err := a.system(c)
	if err != nil {
		return nil, nil, err
	}
	r := detect.Collect(s, detect.Options{Version: Version, Metadata: c.metadata, ProbeSudo: probeSudo})
	return s, r, nil
}

func jsonBytes(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

func (a *app) emit(v any) {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (a *app) fail(format string, args ...any) int {
	fmt.Fprintf(a.err, "kvmx: "+format+"\n", args...)
	return exitFail
}

// ---- terminal styling ----

func (a *app) paint(code, s string) string {
	if !a.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (a *app) modeStyled(m detect.Mode) string {
	switch m {
	case detect.ModeHardware:
		return a.paint("1;32", string(m))
	case detect.ModeNested:
		return a.paint("1;36", string(m))
	}
	return a.paint("1;33", string(m))
}

// confirm asks on a TTY; without a TTY it refuses unless --yes was given.
func (a *app) confirm(prompt string, c *common) bool {
	if c.yes {
		return true
	}
	if !a.tty {
		fmt.Fprintln(a.err, "kvmx: refusing to change the system without confirmation; re-run with --yes")
		return false
	}
	fmt.Fprintf(a.out, "%s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Fscanln(a.in, &ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
