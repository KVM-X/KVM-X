# Benchmarks

All numbers come from `kvmx bench` on one machine and are stored as raw JSON in [`results/`](results/).
They compare **tuning options for the software fallback** and put KVM and user-mode Linux next to it for scale.
They are not a general verdict on emulation: one host, one small guest, a few synthetic workloads.

## Method

* **Host**: AMD Ryzen 5 3400G (4 cores / 8 threads), Arch Linux, kernel 7.x, QEMU 11.1.3, a desktop session running alongside
  (so the host is *not* quiet; see "Noise" below). The KVM row is real hardware KVM on this same machine.
* **Guest**: Alpine 3.24.4 `linux-virt` kernel + initramfs (sha256-pinned in `internal/guest`), with a replacement `/init` appended
  as a second cpio archive. 512 MiB RAM, no network, serial console, direct kernel boot.
* **Metrics** (all but the first are measured *inside* the guest from `/proc/uptime`, so host scheduling noise does not enter them):
  * *Boot ms*: host wall-clock from process start until the guest prints `KVMX-BOOT-OK`.
  * *Shell loop s*: a 100,000-iteration `while` loop in `ash` (interpreter-bound).
  * *sha256 16 MB s*: `dd if=/dev/zero bs=1M count=16 | sha256sum` (native-code-bound, two processes).
  * *Parallel s*: one 50,000-iteration shell loop **per vCPU**, started together; ideal scaling keeps this constant as vCPUs grow.
  * *200k syscalls s*: `dd bs=1 count=100000` to `/dev/null` (one read + one write per byte).
* **Procedure**: 5 passes; each pass boots every configuration once, in the same order (interleaved, so slow drift hits all rows
  alike). The median of 5 is reported; the boot range shows the spread.
* **Configurations** change one factor at a time from `tcg-baseline`: single-threaded TCG, `-cpu qemu64`, machine `pc`, 1 vCPU, default
  translation cache.

Reproduce: `kvmx bench --runs 5 --include-kvm --out my.json` (add `--include-uml --only uml` inside a Debian container with
`user-mode-linux` and `--tmpfs /tmp:exec` for the UML row), then `python3 scripts/benchmd.py`.

## Results

| Config | Boot ms (median) | Boot range | vs baseline | Shell loop s | sha256 16 MB s | Parallel s | 200k syscalls s | What it changes |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `kvm` | 1355 | 1325-2423 | 0.28x | 0.31 | 0.11 | 0.16 | 0.03 | control: hardware/nested KVM |
| `tcg-baseline` | 4772 | 4069-5141 | 1.00x | 4.13 | 0.72 | 2.21 | 0.28 | single thread, qemu64, pc, 1 vCPU, default tb |
| `cpu-haswell` | 5279 | 4573-7619 | 1.11x | 5.63 | 0.96 | 2.59 | 5.44 | baseline + -cpu Haswell (x86-64-v3 guests) |
| `cpu-max` | 5916 | 4734-10737 | 1.24x | 3.97 | 0.93 | 2.07 | 0.28 | baseline + -cpu max |
| `tb-32` | 5187 | 4835-7941 | 1.09x | 4.23 | 0.96 | 2.27 | 0.30 | cpu max, tb-size 32 MiB |
| `tb-256` | 5540 | 5020-8636 | 1.16x | 3.92 | 0.92 | 1.90 | 0.26 | cpu max, tb-size 256 MiB |
| `tb-1024` | 5433 | 4847-10251 | 1.14x | 3.65 | 1.13 | 2.20 | 0.30 | cpu max, tb-size 1024 MiB |
| `single-smp4` | 5668 | 5345-12164 | 1.19x | 4.36 | 0.90 | **8.35** | 0.29 | cpu max, tb 256, single thread, 4 vCPUs |
| `mttcg-smp2` | 5073 | 4414-5992 | 1.06x | 4.07 | 0.72 | 2.41 | 0.30 | cpu max, tb 256, multi-thread, 2 vCPUs |
| `mttcg-smp4` | 5591 | 4777-6423 | 1.17x | 3.95 | 0.83 | **2.25** | 0.28 | cpu max, tb 256, multi-thread, 4 vCPUs |
| `machine-q35` | 4926 | 4329-5523 | 1.03x | 3.77 | 0.65 | 1.97 | 0.29 | cpu max, tb 256, multi-thread, q35, 2 vCPUs |
| `machine-microvm` | 5547 | 3900-6557 | 1.16x | 3.87 | 0.78 | 2.14 | 0.31 | cpu max, tb 256, multi-thread, microvm, 2 vCPUs |
| `cmdline-tuned` | 5353 | 4320-7322 | 1.12x | 4.73 | 0.77 | 2.18 | 0.30 | mttcg-smp2 + quiet/nokaslr/nowatchdog kernel args |
| `profile-compat` | 4717 | 4408-7807 | 0.99x | 3.69 | 0.68 | 1.94 | 0.29 | shipped profile 'compat' |
| `profile-balanced` | 5323 | 4487-5608 | 1.12x | 4.63 | 0.73 | 2.27 | 0.27 | shipped profile 'balanced' (default) |
| `uml` | 1049 | 1000-1099 | 0.22x | 0.37 | 0.14 | 0.19 | **3.65** | user-mode Linux (not a VM; Linux guests only) |

Scale against hardware KVM on the same machine (ratio of times, higher means slower):

| | boot | shell loop | sha256 | parallel | syscalls |
| --- | ---: | ---: | ---: | ---: | ---: |
| TCG `tcg-baseline` | 3.5x | 13.3x | 6.5x | 13.8x | 9.3x |
| TCG `profile-balanced` | 3.9x | 14.9x | 6.6x | 14.2x | 9.0x |
| user-mode Linux | 0.8x | 1.2x | 1.3x | 1.2x | **121.7x** |

## What the data supports

1. **Software emulation costs roughly an order of magnitude of CPU-bound speed** (6x-15x in these workloads), and about 3.5-4x on
   boot. Nothing below changes that. The "5-30x" printed by `kvmx status` brackets this and heavier vector/crypto code.
2. **Multi-threaded TCG is the one option that matters, for SMP guests.** With 4 vCPUs running 4 parallel workers, single-threaded
   TCG took 8.35 s and multi-threaded TCG 2.25 s (3.7x faster; ideal is ~2.1 s, the 1-vCPU time). With 1 vCPU it changes nothing.
   It is on in every shipped profile except `compat` (which is 1 vCPU by design).
3. **`-cpu max` costs boot time and some compute** (about +15-25% boot and +30-55% on the sha256 row at equal vCPU count, measured in two
   independent runs) because the guest then finds and uses AVX/SHA code paths that TCG emulates slowly. It is the default
   because modern distributions need x86-64-v2/v3 instructions that `qemu64` lacks; `--tcg-profile compat` trades that for speed.
4. **Named Intel CPU models can be much worse than either.** `-cpu Haswell` made guest *system calls* 19x slower (5.44 s vs 0.28 s):
   the model advertises no hardware immunity, so the guest kernel enables its Spectre/Meltdown/MDS mitigations. kvmx never picks a
   named model by itself; `max` and `qemu64` do not trigger this.
5. **Translation-block cache size (`tb-size`) made no measurable difference** from 32 to 1024 MiB for this short workload; the 256 MiB
   in `balanced` is a safety margin for longer-running guests, not a measured optimum.
6. **Machine type (`pc`, `q35`, `microvm`) and trimmed kernel command lines are within noise.** The "tuned" command line was not faster
   than the plain one, so no shipped profile uses it (the mechanism stays so you can test it on your workload with `bench`).
7. **User-mode Linux is the odd one out**: it boots in about a second and runs guest userland at native speed (loop 0.37 s, equal to
   KVM within noise), but each system call goes through ptrace: 3.65 s for 200k calls, 13x *slower than TCG* and ~120x slower than KVM.
   Good for compute and quick Linux sandboxes, wrong for syscall- or I/O-heavy work, and it is not a VM (Linux guests only, one vCPU).

## Noise, and what is not claimed

The host was shared with a normal desktop session, and the boot range column shows it: individual boots occasionally take twice
as long as the median. Two rows with *identical* QEMU configurations (`tcg-baseline` and `profile-compat`) measured 4772 ms and
4717 ms here, and in an earlier, non-interleaved run that overlapped other host activity they differed by 39% (5,981 vs 4,294 ms in a
single pass), which is why the final run is interleaved. **Treat differences under ~20% as noise.** The conclusions above rest on
effects far larger than that (KVM vs TCG, MTTCG at 4 vCPUs, the Haswell syscall cost, UML syscalls) or on agreement across two
independent runs (`cpu-max`).

Not measured: disk and network throughput, memory-bandwidth-bound work, guests other than Alpine, more than 4 vCPUs, other hosts, aarch64,
long-running JIT-warmed workloads (where `tb-size` may matter), or nested KVM (this host is bare metal; the NESTED row is documented
as "5-30% on CPU-bound work, worse on VM exits and I/O" from general experience, not measured here).
Please send `kvmx bench --out` JSON from other hosts.

## Boot-time proof

[`results/boot-time.json`](results/boot-time.json) is written by the integration test `TestTCGBootsRealGuest`: a real Alpine kernel booted
under `SOFTWARE-EMULATION (TCG)` through `kvmx run --demo --mode tcg --guest-bench`, from process start to guest power-off.
The same test runs on GitHub Actions for every push (see the `integration` job).
