# Security policy

## Reporting a vulnerability

Please report security problems privately through GitHub:
**Security tab → "Report a vulnerability"** on this repository.
Do not open a public issue for anything that could be exploited.

You can expect an acknowledgement within a few days. Fixes ship in a patch
release and are credited in the release notes unless you ask otherwise.

## What kvmx touches

kvmx is a local tool that runs as your user and escalates with `sudo` only for
individual commands, after showing them to you. It:

* reads `/proc`, `/sys`, `/etc/os-release`, `/etc/group` and runs a few read-only
  probe commands (`systemd-detect-virt`, `qemu-system-* --version`, ...);
* opens `/dev/kvm` and issues two harmless ioctls (`KVM_GET_API_VERSION`,
  `KVM_CREATE_VM`);
* makes system changes **only** through `kvmx enable`, which lists every file and
  command first, asks for confirmation (or `--yes`), and records each change in
  `~/.local/state/kvmx/journal.jsonl` so `kvmx revert` can undo it;
* has **no telemetry**. The network is used only for: downloading the pinned test
  guest (URLs and sizes are printed, sha256 verified, confirmation required),
  downloading release binaries in `install.sh`, your package manager when you let
  `kvmx enable` install QEMU, and, only with `--cloud-metadata`, one request to the
  link-local metadata address `169.254.169.254` (the code refuses any other host).

## Things worth knowing

* **Shims.** `kvmx enable --shims yes` (or automatically when stuck on software
  emulation) symlinks `qemu-system-*`, `kvm`, `virsh` and `virt-install` to the kvmx
  binary in `/usr/local/bin`. They exec the real tool, rewriting only KVM-specific
  options. Remove them with `kvmx revert`.
* **`/dev/kvm` access.** The access fix adds your user to the `kvm` group, installs a
  udev rule with mode `0660`, and sets an ACL for your user. It never uses `0666`.
* **Nested virtualization** enlarges the attack surface of a host; leave `nested=N`
  (`kvmx enable --no-nested`) on hosts that should not run guest hypervisors.
* **Software emulation (TCG)** is not a security boundary any weaker than KVM, but it
  is slow; do not use it to "sandbox" untrusted workloads and expect real-time
  behaviour.
* **Release verification.** Every release publishes `SHA256SUMS`; `install.sh`
  verifies the download against it.
