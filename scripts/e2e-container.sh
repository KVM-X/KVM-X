#!/bin/sh
# End-to-end check run INSIDE a distro container (as root, no /dev/kvm):
#   enable (installs QEMU with the distro's package manager, installs shims)
#   -> boot the Alpine test guest under TCG with `kvmx run`
#   -> boot it through the qemu-system-* shim using a KVM-style command line
#   -> revert (removes shims and the packages kvmx installed)
# Expects: /usr/local/bin/kvmx mounted, KVMX_CACHE_DIR=/cache with the guest already downloaded.
set -eu

fail() { echo "E2E FAIL: $*" >&2; exit 1; }

kvmx version
kvmx doctor --json >/dev/null || fail "doctor"

echo "== status before: expect software emulation / not ready"
rc=0
kvmx status || rc=$?
[ "$rc" -eq 20 ] || [ "$rc" -eq 10 ] || fail "status before enable returned $rc"

echo "== enable"
kvmx enable --yes --no-nested --shims yes

echo "== status after enable: must be TCG, never KVM"
rc=0
kvmx status || rc=$?
[ "$rc" -eq 10 ] || fail "expected exit 10 (software emulation) after enable, got $rc"

echo "== run --demo under TCG"
kvmx run --demo --mode tcg --yes --timeout 300s --guest-bench --json >/tmp/run.json || fail "run --demo"
grep -q '"expect_matched": true' /tmp/run.json || fail "guest did not finish booting"
grep -q 'SOFTWARE-EMULATION (TCG)' /tmp/run.json || fail "run did not report TCG"
grep -E '"(elapsed_ms|expect_ms)"' /tmp/run.json

echo "== boot through the qemu-system-x86_64 shim with -enable-kvm -cpu host"
kernel=$(find /cache/guest -name vmlinuz-virt -path '*x86_64*' | head -n 1)
initrd=$(find /cache/guest/overlay -name 'initrd-*.img' | head -n 1)
[ -n "$kernel" ] && [ -n "$initrd" ] || fail "guest files missing from /cache"
KVMX_FORCE_TCG=1 KVMX_VERBOSE=1 timeout 300 qemu-system-x86_64 -enable-kvm -cpu host -m 512 -smp 2 \
  -nographic -no-reboot -kernel "$kernel" -initrd "$initrd" -append "console=ttyS0" >/tmp/shim.log 2>&1 || true
grep -q "KVMX-DONE" /tmp/shim.log || { tail -20 /tmp/shim.log; fail "shim boot did not reach KVMX-DONE"; }
grep -q "SOFTWARE EMULATION" /tmp/shim.log || fail "shim did not announce software emulation"

echo "== revert"
kvmx revert --yes --remove-packages
if [ -e /usr/local/bin/qemu-system-x86_64 ]; then fail "shim survived revert"; fi
rc=0
kvmx status || rc=$?
[ "$rc" -eq 20 ] || fail "expected exit 20 (QEMU removed) after revert, got $rc"
echo "E2E OK"
