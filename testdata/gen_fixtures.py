#!/usr/bin/env python3
"""Generate the detection fixtures under testdata/fixtures/.

Each fixture mirrors the bits of /proc, /sys and /etc that kvmx reads (root/)
plus meta.json for what files cannot express (exec output, device nodes, uid)
and expect.json with the verdict the detection tests assert.

Run:  python3 testdata/gen_fixtures.py
To add a platform: add a scenario below, regenerate, commit the output.
"""
import json
import os
import shutil

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixtures")

INTEL = ("GenuineIntel", "Intel(R) Xeon(R) Platinum 8375C CPU @ 2.90GHz")
AMD = ("AuthenticAMD", "AMD EPYC 7B13")
BASE_FLAGS = "fpu vme de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov pat pse36 clflush mmx fxsr sse sse2 ht syscall nx lm constant_tsc rep_good nopl cpuid pni pclmulqdq ssse3 fma cx16 sse4_1 sse4_2 x2apic movbe popcnt aes xsave avx f16c rdrand lahf_lm abm"


def cpuinfo_x86(vendor, model, flags, n=2):
    blocks = []
    for i in range(n):
        blocks.append(
            f"processor\t: {i}\nvendor_id\t: {vendor}\ncpu family\t: 6\nmodel\t\t: 106\n"
            f"model name\t: {model}\nflags\t\t: {BASE_FLAGS} {flags}\n"
        )
    return "\n".join(blocks)


def cpuinfo_arm(n=2):
    blocks = []
    for i in range(n):
        blocks.append(
            f"processor\t: {i}\nBogoMIPS\t: 243.75\nFeatures\t: fp asimd evtstrm aes pmull sha1 sha2 crc32 atomics\n"
            "CPU implementer\t: 0x41\nCPU architecture: 8\nCPU variant\t: 0x3\nCPU part\t: 0xd0c\n"
        )
    return "\n".join(blocks)


STATUS = "Name:\tkvmx\nCapEff:\t{cap}\nCapBnd:\t{cap}\nNoNewPrivs:\t0\nSeccomp:\t{seccomp}\n"
CAP_FULL = "000001ffffffffff"
CAP_DOCKER = "00000000a80425fb"  # docker default set: no SYS_MODULE, no MKNOD? (MKNOD is in default set)
CAP_DOCKER_NOMKNOD = "00000000a00425fb"
CAP_USER = "0000000000000000"
MOUNTINFO_RW = "25 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw\n26 25 0:6 / /sys rw,nosuid,nodev,noexec - sysfs sysfs rw\n"
MOUNTINFO_RO = "25 1 0:50 / / rw - overlay overlay rw\n26 25 0:6 / /sys ro,nosuid,nodev,noexec - sysfs sysfs ro\n"


def S(name, **kw):
    d = dict(
        arch="amd64", cpu=("intel", ""), uid=1000, user="dev", groups=["dev"], gids=[1000],
        dmi={}, vm="none", container="none", files={}, devices={}, probe={}, cmds=None,
        kernel="6.8.0-45-generic", loaded=[], nested=None, modules=["kvm-intel", "kvm-amd", "kvm"],
        builtin=[], cap=CAP_USER, seccomp=0, mountinfo=MOUNTINFO_RW, osrel="ubuntu", expect={},
        env={}, misc=True, http={}, extra_exec={},
    )
    d.update(kw)
    d["name"] = name
    return d


KVM_DEV = {"/dev/kvm": {"mode": "0660", "major": 10, "minor": 232, "uid": 0, "gid": 108, "group": "kvm"}}
PROBE_OK = {"opened": True, "api_version": 12, "create_vm_ok": True}
QEMU_CMDS = ["/usr/bin/qemu-system-x86_64", "/usr/bin/qemu-img", "/usr/bin/apt-get", "/usr/bin/sudo"]

SCENARIOS = [
    S("baremetal-intel-kvm-ok", cpu=("intel", "vmx ept vpid"), groups=["dev", "kvm"], gids=[1000, 108],
      dmi={"sys_vendor": "Dell Inc.", "product_name": "PowerEdge R640"}, devices=KVM_DEV, probe=PROBE_OK,
      loaded=["kvm_intel", "kvm"], nested="Y", cmds=QEMU_CMDS,
      expect=dict(mode="HARDWARE-KVM", potential="HARDWARE-KVM", blockers=[], ready=True, provider="bare-metal")),
    S("baremetal-amd-no-kvm-group", cpu=("amd", "svm npt"), dmi={"sys_vendor": "ASUS", "product_name": "PRIME"},
      devices=KVM_DEV, probe={"opened": False, "open_error": "permission denied"}, loaded=["kvm_amd", "kvm"], nested="1",
      cmds=QEMU_CMDS + ["/usr/sbin/usermod", "/usr/sbin/groupadd", "/usr/bin/setfacl"],
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="HARDWARE-KVM", blockers=["dev-kvm-permission"], ready=True,
                  provider="bare-metal")),
    S("baremetal-intel-module-not-loaded", cpu=("intel", "vmx ept vpid"), uid=0, user="root", groups=["root"], gids=[0],
      dmi={"sys_vendor": "Supermicro"}, cap=CAP_FULL, cmds=QEMU_CMDS, vm="none",
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="HARDWARE-KVM", blockers=["kvm-module-not-loaded"], ready=True)),
    S("baremetal-nested-off", cpu=("amd", "svm npt"), groups=["dev", "kvm"], gids=[1000, 108],
      dmi={"sys_vendor": "Gigabyte"}, devices=KVM_DEV, probe=PROBE_OK, loaded=["kvm_amd", "kvm"], nested="N", cmds=QEMU_CMDS,
      expect=dict(mode="HARDWARE-KVM", potential="HARDWARE-KVM", blockers=["nested-param-off"], ready=True)),
    S("kvm-guest-nested-ok", cpu=("intel", "vmx ept vpid hypervisor"), groups=["dev", "kvm"], gids=[1000, 108], vm="kvm",
      dmi={"sys_vendor": "QEMU", "product_name": "Standard PC (Q35 + ICH9, 2009)"}, devices=KVM_DEV, probe=PROBE_OK,
      loaded=["kvm_intel", "kvm"], nested="Y", cmds=QEMU_CMDS,
      expect=dict(mode="NESTED-KVM", potential="NESTED-KVM", blockers=[], ready=True, provider="qemu")),
    S("kvm-guest-dev-kvm-missing", cpu=("intel", "vmx ept vpid hypervisor"), vm="kvm",
      dmi={"sys_vendor": "QEMU", "product_name": "Standard PC (i440FX + PIIX, 1996)"}, loaded=["kvm_intel", "kvm"],
      nested="Y", cmds=QEMU_CMDS,
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="NESTED-KVM", blockers=["dev-kvm-missing"], ready=True)),
    S("proxmox-kvm64-cpu-type", cpu=("intel", "hypervisor"), vm="kvm", model="QEMU Virtual CPU version 2.5+",
      dmi={"sys_vendor": "QEMU", "product_name": "Standard PC (i440FX + PIIX, 1996)"}, cmds=QEMU_CMDS,
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="SOFTWARE-EMULATION (TCG)", blockers=["cpu-no-virt-ext"],
                  ready=True, provider="qemu", note_contains="CPU type")),
    S("gcp-no-nested", cpu=("intel", "hypervisor"), vm="google",
      dmi={"sys_vendor": "Google", "product_name": "Google Compute Engine", "bios_vendor": "Google"}, cmds=QEMU_CMDS,
      http={"GET http://169.254.169.254/computeMetadata/v1/instance/machine-type": "projects/1234/machineTypes/e2-medium"},
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="SOFTWARE-EMULATION (TCG)", blockers=["cpu-no-virt-ext"],
                  ready=True, provider="gcp", instance_type="e2-medium")),
    S("aws-nitro-no-nested", cpu=("intel", "hypervisor"), vm="amazon",
      dmi={"sys_vendor": "Amazon EC2", "product_name": "c5.large", "bios_vendor": "Amazon EC2"}, cmds=QEMU_CMDS,
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="SOFTWARE-EMULATION (TCG)", blockers=["cpu-no-virt-ext"],
                  ready=True, provider="aws")),
    S("azure-nested-module-not-loaded", cpu=("intel", "vmx ept vpid hypervisor"), uid=0, user="root", groups=["root"], gids=[0],
      cap=CAP_FULL, vm="microsoft",
      dmi={"sys_vendor": "Microsoft Corporation", "product_name": "Virtual Machine",
           "chassis_asset_tag": "7783-7084-3265-9085-8269-3286-77"}, cmds=QEMU_CMDS,
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="NESTED-KVM", blockers=["kvm-module-not-loaded"], ready=True,
                  provider="azure")),
    S("oracle-vm-no-virt", cpu=("amd", "hypervisor"), vm="kvm",
      dmi={"sys_vendor": "QEMU", "product_name": "Standard PC (i440FX + PIIX, 1996)", "chassis_asset_tag": "OracleCloud.com"},
      cmds=[],
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="SOFTWARE-EMULATION (TCG)",
                  blockers=["cpu-no-virt-ext", "qemu-missing"], ready=False, provider="oracle")),
    S("docker-no-kvm-device", cpu=("amd", "svm npt"), uid=0, user="root", groups=["root"], gids=[0], container="docker",
      cap=CAP_DOCKER_NOMKNOD, seccomp=2, mountinfo=MOUNTINFO_RO, loaded=["kvm_amd", "kvm"], nested="1", cmds=QEMU_CMDS[:2],
      files={"/.dockerenv": ""}, osrel="alpine", dmi={"sys_vendor": "ASUS"},
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="SOFTWARE-EMULATION (TCG)", blockers=["container-no-kvm-device"],
                  ready=True, container="docker")),
    S("docker-kvm-ok", cpu=("amd", "svm npt"), uid=0, user="root", groups=["root"], gids=[0], container="docker",
      cap=CAP_DOCKER, seccomp=2, mountinfo=MOUNTINFO_RO, loaded=["kvm_amd", "kvm"], nested="1", cmds=QEMU_CMDS[:2],
      files={"/.dockerenv": ""}, devices={"/dev/kvm": {"mode": "0666", "major": 10, "minor": 232, "uid": 0, "gid": 0, "group": "root"}},
      probe=PROBE_OK, osrel="alpine", dmi={"sys_vendor": "ASUS"},
      expect=dict(mode="HARDWARE-KVM", potential="HARDWARE-KVM", blockers=[], ready=True, container="docker")),
    S("openvz-container", cpu=("intel", "vmx ept vpid"), uid=0, user="root", groups=["root"], gids=[0], modules=[],
      files={"/proc/vz/veinfo": "", "/proc/user_beancounters": ""}, cmds=QEMU_CMDS, osrel="centos",
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="SOFTWARE-EMULATION (TCG)", blockers=["kvm-kernel-missing"],
                  ready=True, container="openvz")),
    S("xen-pv-guest", cpu=("intel", "hypervisor"), vm="xen", files={"/proc/xen/capabilities": ""},
      dmi={"sys_vendor": "Xen", "product_name": "HVM domU"}, cmds=QEMU_CMDS,
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="SOFTWARE-EMULATION (TCG)", blockers=["cpu-no-virt-ext"],
                  ready=True, provider="xen")),
    S("wsl2-nested-ok", cpu=("amd", "svm npt hypervisor"), vm="microsoft", container="wsl", kernel="6.6.36.6-microsoft-standard-WSL2",
      groups=["dev", "kvm"], gids=[1000, 108], devices=KVM_DEV, probe=PROBE_OK, loaded=["kvm_amd", "kvm"], nested="1",
      cmds=QEMU_CMDS, modules=[],
      expect=dict(mode="NESTED-KVM", potential="NESTED-KVM", blockers=[], ready=True, container="wsl")),
    S("arm64-baremetal-kvm-ok", arch="arm64", cpu=("arm", ""), groups=["dev", "kvm"], gids=[1000, 108], loaded=["kvm"],
      modules=["kvm"], devices=KVM_DEV, probe=PROBE_OK, dmi={"sys_vendor": "Ampere"},
      cmds=["/usr/bin/qemu-system-aarch64", "/usr/bin/apt-get"],
      expect=dict(mode="HARDWARE-KVM", potential="HARDWARE-KVM", blockers=[], ready=True)),
    S("arm64-cloud-no-kvm", arch="arm64", cpu=("arm", ""), vm="amazon", dmi={"sys_vendor": "Amazon EC2", "product_name": "c7g.large"},
      modules=[], cmds=["/usr/bin/qemu-system-aarch64", "/usr/bin/apt-get"],
      expect=dict(mode="SOFTWARE-EMULATION (TCG)", potential="SOFTWARE-EMULATION (TCG)", blockers=["kvm-kernel-missing"],
                  ready=True, provider="aws")),
    S("kvm-ok-qemu-missing", cpu=("intel", "vmx ept vpid"), groups=["dev", "kvm"], gids=[1000, 108],
      dmi={"sys_vendor": "Dell Inc."}, devices=KVM_DEV, probe=PROBE_OK, loaded=["kvm_intel", "kvm"], nested="Y",
      cmds=["/usr/bin/apt-get"],
      expect=dict(mode="HARDWARE-KVM", potential="HARDWARE-KVM", blockers=["qemu-missing"], ready=False)),
]


def w(root, path, text):
    p = os.path.join(root, path.lstrip("/"))
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w") as f:
        f.write(text)


def build(sc):
    d = os.path.join(OUT, sc["name"])
    shutil.rmtree(d, ignore_errors=True)
    root = os.path.join(d, "root")
    os.makedirs(root)
    kind, flags = sc["cpu"]
    if kind == "arm":
        w(root, "/proc/cpuinfo", cpuinfo_arm())
    else:
        vendor, model = INTEL if kind == "intel" else AMD
        model = sc.get("model", model)
        w(root, "/proc/cpuinfo", cpuinfo_x86(vendor, model, flags))
    rel = sc["kernel"]
    w(root, "/proc/sys/kernel/osrelease", rel + "\n")
    w(root, "/proc/self/status", STATUS.format(cap=sc["cap"], seccomp=sc["seccomp"]))
    w(root, "/proc/self/mountinfo", sc["mountinfo"])
    w(root, "/proc/1/cgroup", "0::/\n")
    w(root, "/etc/os-release", f'ID={sc["osrel"]}\nVERSION_ID="1"\nPRETTY_NAME="{sc["osrel"]}"\n')
    if sc["misc"]:
        w(root, "/proc/misc", " 58 vhost-vsock\n232 kvm\n")
    for k, v in sc["dmi"].items():
        w(root, f"/sys/class/dmi/id/{k}", v + "\n")
    deps = "".join(f"kernel/arch/x86/kvm/{m}.ko.zst: kernel/arch/x86/kvm/kvm.ko.zst\n" if m != "kvm" else "kernel/arch/x86/kvm/kvm.ko.zst:\n"
                   for m in sc["modules"])
    w(root, f"/lib/modules/{rel}/modules.dep", deps)
    w(root, f"/lib/modules/{rel}/modules.builtin", "".join(f"kernel/{b}\n" for b in sc["builtin"]))
    for m in sc["loaded"]:
        os.makedirs(os.path.join(root, "sys/module", m), exist_ok=True)
        if sc["nested"] and m in ("kvm_intel", "kvm_amd"):
            w(root, f"/sys/module/{m}/parameters/nested", sc["nested"] + "\n")
            w(root, f"/sys/module/{m}/refcnt", "0\n")
    if sc["container"] == "none":
        w(root, "/run/systemd/system/.keep", "")
    if sc["devices"]:
        w(root, "/etc/group", "root:x:0:\nkvm:x:108:\n")
    for p, t in sc["files"].items():
        w(root, p, t)
    cmds = sc["cmds"] if sc["cmds"] is not None else QEMU_CMDS
    vm = sc["vm"]
    ex = {
        "systemd-detect-virt --vm": {"out": vm + "\n", "fail": vm == "none"},
        "systemd-detect-virt --container": {"out": sc["container"] + "\n", "fail": sc["container"] == "none"},
    }
    for c in cmds:
        base = os.path.basename(c)
        if base.startswith("qemu-system-"):
            ex[f"{c} --version"] = {"out": "QEMU emulator version 8.2.2\n"}
            ex[f"{c} -accel help"] = {"out": "Accelerators supported in QEMU binary:\ntcg\nkvm\n"}
    ex.update(sc["extra_exec"])
    meta = dict(goarch=sc["arch"], uid=sc["uid"], username=sc["user"], gids=sc["gids"], groups=sc["groups"], env=sc["env"],
                commands=cmds, exec=ex, devices=sc["devices"], kvm_probe=sc["probe"], http=sc["http"])
    w(d, "meta.json", json.dumps(meta, indent=2) + "\n")
    w(d, "expect.json", json.dumps(sc["expect"], indent=2) + "\n")


if __name__ == "__main__":
    shutil.rmtree(OUT, ignore_errors=True)
    for sc in SCENARIOS:
        build(sc)
    print(f"wrote {len(SCENARIOS)} fixtures to {OUT}")
