package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/KVM-X/KVM-X/internal/detect"
	"github.com/KVM-X/KVM-X/internal/guest"
	"github.com/KVM-X/KVM-X/internal/qemu"
	"github.com/KVM-X/KVM-X/internal/uml"
)

// benchConfig is one row of the matrix: a QEMU configuration to boot.
type benchConfig struct {
	ID      string
	Desc    string
	Mode    string // kvm | tcg
	Profile qemu.Profile
	CPUs    int
	Machine string
}

func p(thread string, tb int, cpu, machine string) qemu.Profile {
	return qemu.Profile{Name: "bench", Thread: thread, TBSizeMB: tb, CPU: cpu, Machine: machine}
}

// matrix varies one TCG factor at a time from a conservative baseline, then
// ends with the shipped profiles.
func matrix(includeKVM, includeUML bool, arch string, quick bool) []benchConfig {
	var m []benchConfig
	if includeKVM {
		m = append(m, benchConfig{ID: "kvm", Desc: "control: hardware/nested KVM", Mode: "kvm", Profile: p("", 0, "", ""), CPUs: 2})
	}
	machine := "pc"
	if arch == "aarch64" {
		machine = "virt"
	}
	tuned := p("multi", 256, "max", machine)
	tuned.Cmdline = "quiet loglevel=3 nokaslr nowatchdog tsc=reliable"
	full := []benchConfig{
		{ID: "tcg-baseline", Desc: "single thread, qemu64, pc, 1 vCPU, default tb", Mode: "tcg", Profile: p("single", 0, "qemu64", machine), CPUs: 1},
		{ID: "cpu-haswell", Desc: "baseline + -cpu Haswell (x86-64-v3 guests)", Mode: "tcg", Profile: p("single", 0, "Haswell", machine), CPUs: 1},
		{ID: "cpu-max", Desc: "baseline + -cpu max", Mode: "tcg", Profile: p("single", 0, "max", machine), CPUs: 1},
		{ID: "tb-32", Desc: "cpu max, tb-size 32 MiB", Mode: "tcg", Profile: p("single", 32, "max", machine), CPUs: 1},
		{ID: "tb-256", Desc: "cpu max, tb-size 256 MiB", Mode: "tcg", Profile: p("single", 256, "max", machine), CPUs: 1},
		{ID: "tb-1024", Desc: "cpu max, tb-size 1024 MiB", Mode: "tcg", Profile: p("single", 1024, "max", machine), CPUs: 1},
		{ID: "single-smp4", Desc: "cpu max, tb 256, single thread, 4 vCPUs", Mode: "tcg", Profile: p("single", 256, "max", machine), CPUs: 4},
		{ID: "mttcg-smp2", Desc: "cpu max, tb 256, multi-thread, 2 vCPUs", Mode: "tcg", Profile: p("multi", 256, "max", machine), CPUs: 2},
		{ID: "mttcg-smp4", Desc: "cpu max, tb 256, multi-thread, 4 vCPUs", Mode: "tcg", Profile: p("multi", 256, "max", machine), CPUs: 4},
		{ID: "machine-q35", Desc: "cpu max, tb 256, multi-thread, q35, 2 vCPUs", Mode: "tcg", Profile: p("multi", 256, "max", "q35"), CPUs: 2, Machine: "q35"},
		{ID: "machine-microvm", Desc: "cpu max, tb 256, multi-thread, microvm, 2 vCPUs", Mode: "tcg", Profile: p("multi", 256, "max", "microvm"), CPUs: 2, Machine: "microvm"},
		{ID: "cmdline-tuned", Desc: "mttcg-smp2 + quiet/nokaslr/nowatchdog kernel args", Mode: "tcg", Profile: tuned, CPUs: 2},
	}
	if arch == "aarch64" {
		full = []benchConfig{
			{ID: "tcg-baseline", Desc: "single thread, 1 vCPU", Mode: "tcg", Profile: p("single", 0, "max", "virt"), CPUs: 1},
			{ID: "mttcg-smp2", Desc: "multi-thread, 2 vCPUs, tb 256", Mode: "tcg", Profile: p("multi", 256, "max", "virt"), CPUs: 2},
		}
	}
	m = append(m, full...)
	for _, name := range []string{"compat", "balanced"} {
		pr := qemu.Profiles[name]
		cpus := pr.MaxCPUs
		if cpus == 0 {
			cpus = 1
		}
		if cpus > 2 {
			cpus = 2
		}
		m = append(m, benchConfig{ID: "profile-" + name, Desc: "shipped profile '" + name + "'", Mode: "tcg", Profile: pr, CPUs: cpus})
	}
	if includeUML {
		m = append(m, benchConfig{ID: "uml", Desc: "user-mode Linux (not a VM; Linux guests only)", Mode: "uml"})
	}
	if quick {
		var q []benchConfig
		for _, c := range m {
			if c.ID == "kvm" || c.ID == "uml" || strings.HasPrefix(c.ID, "profile-") || c.ID == "tcg-baseline" {
				q = append(q, c)
			}
		}
		return q
	}
	return m
}

type benchRow struct {
	ID        string    `json:"id"`
	Desc      string    `json:"description"`
	Mode      string    `json:"mode"`
	Runs      int       `json:"runs_ok"`
	BootMS    float64   `json:"boot_ms_median"`
	TotalMS   float64   `json:"total_ms_median"`
	ShellLoop float64   `json:"guest_shell_loop_s_median"`
	SHA256    float64   `json:"guest_sha256_16mb_s_median"`
	Parallel  float64   `json:"guest_parallel_loop_s_median"`
	Syscalls  float64   `json:"guest_syscalls_s_median"`
	GuestUp   float64   `json:"guest_kernel_boot_s_median"`
	BootRuns  []float64 `json:"boot_ms_runs"`
	Error     string    `json:"error,omitempty"`
}

type benchReport struct {
	Version   string         `json:"kvmx_version"`
	When      string         `json:"when"`
	Host      *detect.Report `json:"host"`
	Guest     string         `json:"guest"`
	Runs      int            `json:"runs_per_config"`
	Rows      []benchRow     `json:"results"`
	Baseline  string         `json:"baseline"`
	Reference string         `json:"reference_note"`
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64{}, v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

var reNum = func(key string) *regexp.Regexp { return regexp.MustCompile(key + `=([0-9.]+)`) }

func grab(re *regexp.Regexp, s string) (float64, bool) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	return f, err == nil
}

func (a *app) cmdBench(args []string) int {
	var c common
	var runs int
	var quick, includeKVM, includeUML bool
	var arch, out, only string
	var timeout time.Duration
	fs := a.flagSet("bench", &c)
	fs.IntVar(&runs, "runs", 3, "boots per configuration (median is reported)")
	fs.BoolVar(&quick, "quick", false, "only baseline + shipped profiles, 1 run")
	fs.BoolVar(&includeKVM, "include-kvm", false, "add a KVM control row when KVM is usable")
	fs.BoolVar(&includeUML, "include-uml", false, "add a user-mode Linux row (needs linux.uml; x86_64 only)")
	fs.StringVar(&only, "only", "", "comma-separated config IDs to run (see --dry-run for the list)")
	fs.StringVar(&arch, "arch", "", "guest architecture (default: this machine's)")
	fs.StringVar(&out, "out", "", "also write the JSON report to this file")
	fs.DurationVar(&timeout, "timeout", 5*time.Minute, "per-boot timeout")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	if quick && runs == 3 {
		runs = 1
	}
	s, err := a.system(&c)
	if err != nil {
		return a.fail("%v", err)
	}
	r := detect.Collect(s, detect.Options{Version: Version})
	if arch == "" {
		arch = r.CPU.Arch
	}
	if includeKVM && (!r.KVM.Usable || arch != r.CPU.Arch) {
		fmt.Fprintln(a.err, "kvmx: --include-kvm ignored: KVM is not usable for this guest here")
		includeKVM = false
	}
	cfgs := matrix(includeKVM, includeUML, arch, quick)
	if only != "" {
		want := map[string]bool{}
		for _, id := range strings.Split(only, ",") {
			want[strings.TrimSpace(id)] = true
		}
		var f []benchConfig
		for _, cf := range cfgs {
			if want[cf.ID] {
				f = append(f, cf)
			}
		}
		cfgs = f
	}

	if c.dry || c.fixture != "" {
		for _, cf := range cfgs {
			fmt.Fprintf(a.out, "%-18s %s  (cpus=%d)\n", cf.ID, cf.Desc, cf.CPUs)
		}
		fmt.Fprintf(a.out, "%d configurations x %d run(s) = %d boots\n", len(cfgs), runs, len(cfgs)*runs)
		return exitOK
	}
	needQEMU, needUML := false, false
	for _, cf := range cfgs {
		needQEMU = needQEMU || cf.Mode != "uml"
		needUML = needUML || cf.Mode == "uml"
	}
	var bin, umlBin, umlRoot, umlTmp, img string
	var paths guest.Paths
	eo := guest.EnsureOpts{Yes: c.yes, TTY: a.tty, Out: a.err, In: a.in}
	if needQEMU {
		var err error
		if bin, err = qemu.Find(s, arch); err != nil {
			return a.fail("%v", err)
		}
		if paths, err = guest.Ensure(arch, eo); err != nil {
			return a.fail("%v", err)
		}
		if img, err = guest.Overlay(paths.Initrd, guest.InitScript, filepath.Join(guest.CacheDir(), "guest", "overlay")); err != nil {
			return a.fail("%v", err)
		}
	}
	if needUML {
		var err error
		if umlBin = uml.Find(s); umlBin == "" {
			return a.fail("--include-uml: linux.uml not found")
		}
		if umlTmp, err = uml.TmpDir(s); err != nil {
			return a.fail("%v", err)
		}
		if umlRoot, err = guest.Rootfs("x86_64", eo); err != nil {
			return a.fail("%v", err)
		}
		if err = os.WriteFile(filepath.Join(umlRoot, "kvmx-init"), []byte(guest.InitScript), 0o755); err != nil { //nolint:gosec // must be executable in the guest
			return a.fail("%v", err)
		}
	}

	rep := benchReport{Version: Version, When: time.Now().UTC().Format(time.RFC3339), Host: r,
		Guest: "Alpine " + guest.AlpineVersion + " linux-virt kernel + initramfs (" + arch + ")", Runs: runs, Baseline: "tcg-baseline",
		Reference: "boot_ms = host wall time from QEMU start until the guest prints KVMX-BOOT-OK; guest_* are measured inside the guest from /proc/uptime"}
	fmt.Fprintf(a.err, "kvmx bench: %d configs x %d runs on %s (%s), guest %s\n", len(cfgs), runs, orDash(r.CPU.Model), r.Mode, rep.Guest)

	reLoop, reSHA, rePar, reSys, reUp := reNum("shell_loop_s"), reNum("sha256_16mb_s"), reNum("parallel_loop_s"), reNum("syscalls_s"), reNum("uptime")
	type acc struct{ boots, totals, loops, shas, pars, sysc, ups []float64 }
	accs := make([]acc, len(cfgs))
	errs := make([]string, len(cfgs))
	// Interleave: run 1 of every config, then run 2 of every config, ... so slow drift
	// on a shared host (thermal state, other tenants) spreads over all rows instead of biasing one.
	for i := 0; i < runs; i++ {
		for k, cf := range cfgs {
			var res procResult
			if cf.Mode == "uml" {
				res = runProcess(umlBin, uml.Args(umlRoot, "/kvmx-init", 256, "kvmx.bench=1"),
					procOpts{Timeout: timeout, Expect: "KVMX-DONE", Marks: []string{"KVMX-BOOT-OK"}, Env: uml.Environment(umlTmp)})
			} else {
				sp := qemu.Spec{Arch: arch, Mode: cf.Mode, Profile: cf.Profile, CPUs: cf.CPUs, MemMB: 512, Machine: cf.Machine,
					Kernel: paths.Kernel, Initrd: img, NoNet: true, Display: "none"}
				sp.Append = "console=" + map[string]string{"x86_64": "ttyS0", "aarch64": "ttyAMA0"}[arch] + " kvmx.bench=1"
				argv, err := sp.Args()
				if err != nil {
					errs[k] = err.Error()
					continue
				}
				res = runProcess(bin, argv, procOpts{Timeout: timeout, Expect: "KVMX-DONE", Marks: []string{"KVMX-BOOT-OK"}})
			}
			if !res.Matched {
				errs[k] = fmt.Sprintf("run %d: guest did not finish (exit %d, timeout %v)", i+1, res.ExitCode, res.TimedOut)
				fmt.Fprintf(a.err, "  %s: %s\n", cf.ID, errs[k])
				continue
			}
			ac := &accs[k]
			ac.boots = append(ac.boots, float64(res.MarkAt["KVMX-BOOT-OK"].Milliseconds()))
			ac.totals = append(ac.totals, float64(res.Elapsed.Milliseconds()))
			for _, m := range []struct {
				re *regexp.Regexp
				to *[]float64
			}{{reLoop, &ac.loops}, {reSHA, &ac.shas}, {rePar, &ac.pars}, {reSys, &ac.sysc}, {reUp, &ac.ups}} {
				if v, ok := grab(m.re, res.Output); ok {
					*m.to = append(*m.to, v)
				}
			}
		}
		fmt.Fprintf(a.err, "  pass %d/%d done\n", i+1, runs)
	}
	for k, cf := range cfgs {
		ac := accs[k]
		row := benchRow{ID: cf.ID, Desc: cf.Desc, Mode: cf.Mode, Error: errs[k], Runs: len(ac.boots), BootRuns: ac.boots,
			BootMS: median(ac.boots), TotalMS: median(ac.totals), ShellLoop: median(ac.loops), SHA256: median(ac.shas),
			Parallel: median(ac.pars), Syscalls: median(ac.sysc), GuestUp: median(ac.ups)}
		rep.Rows = append(rep.Rows, row)
		fmt.Fprintf(a.err, "  %-18s boot %6.0f ms   loop %5.2fs   sha %5.2fs   par %5.2fs   sys %5.2fs\n", cf.ID, row.BootMS, row.ShellLoop, row.SHA256, row.Parallel, row.Syscalls)
	}

	if out != "" {
		if err := writeJSON(out, rep); err != nil {
			return a.fail("%v", err)
		}
	}
	if c.json {
		a.emit(rep)
		return exitOK
	}
	a.printBench(rep)
	return exitOK
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *app) printBench(rep benchReport) {
	var base *benchRow
	for i := range rep.Rows {
		if rep.Rows[i].ID == rep.Baseline {
			base = &rep.Rows[i]
		}
	}
	tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "CONFIG\tBOOT ms\tvs base\tTOTAL ms\tLOOP s\tSHA s\tPAR s\tSYS s\tNOTES")
	for _, r := range rep.Rows {
		rel := "-"
		if base != nil && base.BootMS > 0 && r.BootMS > 0 {
			rel = fmt.Sprintf("%.2fx", r.BootMS/base.BootMS)
		}
		note := r.Desc
		if r.Error != "" {
			note = "ERROR " + r.Error
		}
		fmt.Fprintf(tw, "%s\t%.0f\t%s\t%.0f\t%.2f\t%.2f\t%.2f\t%.2f\t%s\n", r.ID, r.BootMS, rel, r.TotalMS, r.ShellLoop, r.SHA256, r.Parallel, r.Syscalls, note)
	}
	_ = tw.Flush()
	fmt.Fprintln(a.out, "\n"+rep.Reference)
}
