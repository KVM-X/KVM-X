package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/KVM-X/KVM-X/internal/detect"
	"github.com/KVM-X/KVM-X/internal/sys"
)

// cmdCollect records everything detection reads into a fixture directory that
// can be attached to a platform report and replayed with --fixture.
func (a *app) cmdCollect(args []string) int {
	var c common
	var out string
	fs := a.flagSet("collect", &c)
	fs.StringVar(&out, "out", "", "directory to write the recorded fixture to (required)")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	if out == "" {
		return a.fail("--out DIR is required")
	}
	if es, err := os.ReadDir(out); err == nil && len(es) > 0 {
		return a.fail("%s is not empty", out)
	}
	rec := sys.NewRecorder(sys.Host{})
	r := detect.Collect(rec, detect.Options{Version: Version, Metadata: c.metadata})
	if c.dry {
		fmt.Fprintf(a.out, "would record the inputs of detection (%s) into %s\n", r.Mode, out)
		return exitOK
	}
	if err := rec.Save(out); err != nil {
		return a.fail("%v", err)
	}
	if b, err := jsonBytes(r); err == nil {
		_ = os.WriteFile(filepath.Join(out, "detect.json"), b, 0o644)
	}
	fmt.Fprintf(a.out, "Recorded to %s (status here: %s).\n", out, r.Mode)
	fmt.Fprintf(a.out, "Inspect it (it contains CPU flags, DMI vendor strings, module/device facts and tool versions), then zip and attach it to a platform report.\n")
	fmt.Fprintf(a.out, "Replay: kvmx detect --fixture %s\n", out)
	return exitOK
}
