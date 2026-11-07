package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = Main(append([]string{"kvmx"}, args...), strings.NewReader(""), &o, &e)
	return code, o.String(), e.String()
}

func fx(name string) string { return filepath.Join("..", "..", "testdata", "fixtures", name) }

func TestStatusExitCodes(t *testing.T) {
	for name, want := range map[string]int{
		"baremetal-intel-kvm-ok": exitOK, "kvm-guest-nested-ok": exitOK, "docker-no-kvm-device": exitSoftware,
		"gcp-no-nested": exitSoftware, "oracle-vm-no-virt": exitNotReady, "kvm-ok-qemu-missing": exitNotReady,
	} {
		if code, out, _ := run(t, "status", "--fixture", fx(name)); code != want {
			t.Errorf("%s: exit %d want %d (%s)", name, code, want, out)
		}
	}
}

func TestStatusNeverCallsTCGKVM(t *testing.T) {
	for _, name := range []string{"gcp-no-nested", "docker-no-kvm-device", "baremetal-amd-no-kvm-group"} {
		_, out, _ := run(t, "status", "--fixture", fx(name))
		first, _, _ := strings.Cut(out, "\n") // later lines may legitimately say what is reachable
		if !strings.HasPrefix(first, "SOFTWARE-EMULATION (TCG)") || strings.Contains(first, "KVM -") {
			t.Errorf("%s: %q", name, out)
		}
	}
}

func TestDetectAndStatusJSON(t *testing.T) {
	code, out, _ := run(t, "status", "--json", "--fixture", fx("kvm-guest-nested-ok"))
	var s struct {
		Mode     string `json:"mode"`
		ModeCode string `json:"mode_code"`
		Ready    bool   `json:"ready"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil || code != 0 || s.Mode != "NESTED-KVM" || s.ModeCode != "nested-kvm" || !s.Ready {
		t.Errorf("status json: code %d %+v err %v", code, s, err)
	}
	_, out, _ = run(t, "detect", "--json", "--fixture", fx("aws-nitro-no-nested"))
	var r struct {
		Provider struct{ ID string } `json:"provider"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Provider.ID != "aws" {
		t.Errorf("detect json: %v %+v", err, r)
	}
}

func TestEnableDryRunWithFixtureNeverApplies(t *testing.T) {
	t.Setenv("KVMX_STATE_DIR", t.TempDir())
	// --yes without --dry-run would apply for real; a fixture must force dry-run.
	code, out, _ := run(t, "enable", "--yes", "--json", "--fixture", fx("baremetal-amd-no-kvm-group"))
	var res struct {
		Applied bool `json:"applied"`
		Plan    []struct {
			ID string `json:"id"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != 0 || res.Applied || len(res.Plan) == 0 {
		t.Fatalf("code %d applied %v plan %v err %v", code, res.Applied, res.Plan, err)
	}
}

func TestRunDryRunAndModeKVMRefusal(t *testing.T) {
	code, out, _ := run(t, "run", "--demo", "--mode", "tcg", "--dry-run", "--fixture", fx("gcp-no-nested"))
	if code != 0 || !strings.Contains(out, "-accel tcg,thread=multi") || !strings.Contains(out, "qemu-system-x86_64") {
		t.Errorf("dry-run: code %d out %q", code, out)
	}
	code, _, errOut := run(t, "run", "--demo", "--mode", "kvm", "--dry-run", "--fixture", fx("gcp-no-nested"))
	if code == 0 || !strings.Contains(errOut, "--mode kvm requested") {
		t.Errorf("--mode kvm must refuse: code %d %q", code, errOut)
	}
	code, out, _ = run(t, "run", "--demo", "--dry-run", "--fixture", fx("baremetal-intel-kvm-ok"))
	if code != 0 || !strings.Contains(out, "-accel kvm") || strings.Contains(out, "tcg") {
		t.Errorf("auto on a KVM host should use KVM: %q", out)
	}
}

func TestUsageAndUnknown(t *testing.T) {
	if code, _, _ := run(t); code != exitUsage {
		t.Errorf("no args: %d", code)
	}
	if code, _, e := run(t, "frobnicate"); code != exitUsage || !strings.Contains(e, "unknown command") {
		t.Errorf("unknown: %d %q", code, e)
	}
	if code, out, _ := run(t, "version"); code != 0 || !strings.HasPrefix(out, "kvmx ") {
		t.Errorf("version: %d %q", code, out)
	}
}

func TestFlagsCommand(t *testing.T) {
	code, out, _ := run(t, "flags", "docker", "--fixture", fx("docker-no-kvm-device"))
	if code != 0 || !strings.Contains(out, "--device /dev/kvm") || !strings.Contains(out, "c 10:232") {
		t.Errorf("docker flags: %d %q", code, out)
	}
	if code, _, _ := run(t, "flags", "nope"); code != exitUsage {
		t.Errorf("unknown runtime should be a usage error, got %d", code)
	}
}

func TestParseMem(t *testing.T) {
	for in, want := range map[string]int{"512": 512, "512M": 512, "2G": 2048, "1g": 1024} {
		if got, err := parseMem(in); err != nil || got != want {
			t.Errorf("parseMem(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "x", "-1", "0G"} {
		if _, err := parseMem(bad); err == nil {
			t.Errorf("parseMem(%q) should fail", bad)
		}
	}
}

func TestRewriteVirtInstall(t *testing.T) {
	got := strings.Join(rewriteVirtInstall(strings.Fields("--name vm --virt-type kvm --cpu host-passthrough --memory 1024")), " ")
	if got != "--name vm --virt-type qemu --cpu max --memory 1024" {
		t.Errorf("got %q", got)
	}
	if got := strings.Join(rewriteVirtInstall([]string{"--name", "vm"}), " "); got != "--name vm --virt-type qemu" {
		t.Errorf("missing virt-type should be added: %q", got)
	}
}

func TestRemoteURINotRewritten(t *testing.T) {
	for args, want := range map[string]bool{
		"-c qemu+ssh://host/system define x.xml": true, "--connect=qemu://host/system list": true,
		"-c qemu:///system define x.xml": false, "define x.xml": false,
	} {
		if got := remoteURI(strings.Fields(args)); got != want {
			t.Errorf("remoteURI(%q) = %v", args, got)
		}
	}
}

func TestShellJoin(t *testing.T) {
	if got := shellJoin([]string{"a", "b c", "it's"}); got != `a 'b c' 'it'\''s'` {
		t.Errorf("shellJoin = %s", got)
	}
}
