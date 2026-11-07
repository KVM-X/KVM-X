package qemu

import (
	"strings"
	"testing"
)

func TestRewriteForTCG(t *testing.T) {
	p := Profiles["balanced"]
	cases := []struct {
		name string
		in   string
		want string // space-joined
	}{
		{"enable-kvm", "-enable-kvm -m 2G -cpu host", "-accel tcg,thread=multi,tb-size=256 -m 2G -cpu max"},
		{"accel kvm", "-accel kvm -smp 4", "-accel tcg,thread=multi,tb-size=256 -smp 4"},
		{"accel fallback list", "-accel kvm:tcg", "-accel tcg,thread=multi,tb-size=256"},
		{"machine accel", "-machine q35,accel=kvm,kernel_irqchip=on -cpu host,migratable=off,+vmx", "-machine q35,accel=tcg -cpu max"},
		{"M alias", "-M pc,accel=kvm:tcg,usb=off", "-M pc,accel=tcg,usb=off"},
		{"named cpu keeps model", "-enable-kvm -cpu Haswell,kvm=off,hv-relaxed,+avx2", "-accel tcg,thread=multi,tb-size=256 -cpu Haswell,+avx2"},
		{"double dash", "--enable-kvm --cpu host", "-accel tcg,thread=multi,tb-size=256 --cpu max"},
		{"global kvm-pit", "-global kvm-pit.lost_tick_policy=discard -m 1G", "-accel tcg,thread=multi,tb-size=256 -m 1G"},
		{"already tcg untouched", "-accel tcg,thread=single -cpu qemu64", "-accel tcg,thread=single -cpu qemu64"},
		{"no kvm mention", "-m 512 -kernel k", "-accel tcg,thread=multi,tb-size=256 -m 512 -kernel k"},
		{"plain args pass through", "-drive file=a.img,format=raw -enable-kvm -nographic", "-accel tcg,thread=multi,tb-size=256 -drive file=a.img,format=raw -nographic"},
	}
	for _, c := range cases {
		got, _ := RewriteForTCG(strings.Fields(c.in), p)
		if g := strings.Join(got, " "); g != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, g, c.want)
		}
	}
}

func TestRewriteDomainXML(t *testing.T) {
	in := `<domain type='kvm'>
  <name>vm1</name>
  <features><acpi/><hyperv><relaxed state='on'/></hyperv><kvm><hidden state='on'/></kvm></features>
  <cpu mode='host-passthrough' check='none'/>
  <clock offset='utc'><timer name='kvmclock' present='yes'/><timer name='rtc' tickpolicy='catchup'/></clock>
</domain>`
	out, changes := RewriteDomainXML(in)
	for _, bad := range []string{"type='kvm'", "host-passthrough", "<hyperv", "<kvm", "kvmclock"} {
		if strings.Contains(out, bad) {
			t.Errorf("output still contains %q:\n%s", bad, out)
		}
	}
	for _, good := range []string{"<domain type='qemu'>", "<model fallback='allow'>max</model>", "name='rtc'"} {
		if !strings.Contains(out, good) {
			t.Errorf("output lacks %q:\n%s", good, out)
		}
	}
	if len(changes) < 4 {
		t.Errorf("changes = %v", changes)
	}
	if again, c2 := RewriteDomainXML(out); again != out || len(c2) != 0 {
		t.Errorf("rewrite is not idempotent: %v", c2)
	}
}

func TestGenerateDomainXML(t *testing.T) {
	x := GenerateDomainXML(DomainOpts{Name: "t", MemMB: 512, CPUs: 2, Disk: "/v/a.qcow2", Mode: "tcg", TBCacheMB: 256})
	for _, w := range []string{"<domain type='qemu'>", "<tb-cache unit='MiB'>256", "type='qcow2'", "<model fallback='allow'>max</model>"} {
		if !strings.Contains(x, w) {
			t.Errorf("missing %q in\n%s", w, x)
		}
	}
	if !strings.Contains(GenerateDomainXML(DomainOpts{Name: "t", MemMB: 1, Mode: "kvm"}), "host-passthrough") {
		t.Error("kvm domain should use host-passthrough")
	}
}

func TestArgsTCGvsKVM(t *testing.T) {
	sp := Spec{Arch: "x86_64", Mode: "tcg", Profile: Profile{Name: "t", Thread: "multi", TBSizeMB: 512, CPU: "max", Machine: "q35", Cmdline: "quiet"}, Kernel: "/k", Initrd: "/i", Disk: "/d.qcow2", HostFwd: []string{"tcp::2222-:22"}}
	a, err := sp.Args()
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(a, " ")
	for _, w := range []string{"-accel tcg,thread=multi,tb-size=512", "-cpu max", "format=qcow2", "virtio-blk-pci", "hostfwd=tcp::2222-:22", "console=ttyS0 quiet"} {
		if !strings.Contains(j, w) {
			t.Errorf("missing %q in %s", w, j)
		}
	}
	sp.Mode = "kvm"
	a, _ = sp.Args()
	j = strings.Join(a, " ")
	if !strings.Contains(j, "-accel kvm") || !strings.Contains(j, "-cpu host") || strings.Contains(j, "tcg") || strings.Contains(j, "quiet") {
		t.Errorf("kvm args wrong: %s", j)
	}
	if _, err := (&Spec{Arch: "x86_64", Mode: "tcg"}).Args(); err == nil {
		t.Error("expected error when nothing to boot")
	}
	sp = Spec{Arch: "x86_64", Mode: "tcg", Kernel: "/k", Machine: "microvm"}
	a, _ = sp.Args()
	if j = strings.Join(a, " "); !strings.Contains(j, "virtio-net-device") || strings.Contains(j, "virtio-net-pci") {
		t.Errorf("microvm should use mmio devices: %s", j)
	}
}
