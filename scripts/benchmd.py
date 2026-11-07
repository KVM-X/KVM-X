#!/usr/bin/env python3
"""Render docs/results/bench-*.json as the Markdown table used in docs/BENCHMARKS.md."""
import json
import os

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "docs", "results")


def load(name):
    with open(os.path.join(ROOT, name)) as f:
        return json.load(f)


def main():
    m, u = load("bench-matrix.json"), load("bench-uml.json")
    rows = m["results"] + u["results"]
    base = next(r for r in rows if r["id"] == "tcg-baseline")
    kvm = next(r for r in rows if r["id"] == "kvm")
    print("| Config | Boot ms (median) | Boot range | vs baseline | Shell loop s | sha256 16 MB s | Parallel s | 200k syscalls s | What it changes |")
    print("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |")
    for r in rows:
        runs = r["boot_ms_runs"]
        rel = f"{r['boot_ms_median'] / base['boot_ms_median']:.2f}x"
        print(f"| `{r['id']}` | {r['boot_ms_median']:.0f} | {min(runs):.0f}-{max(runs):.0f} | {rel} | {r['guest_shell_loop_s_median']:.2f} | "
              f"{r['guest_sha256_16mb_s_median']:.2f} | {r['guest_parallel_loop_s_median']:.2f} | {r['guest_syscalls_s_median']:.2f} | {r['description']} |")
    print()
    for r in rows:
        if r["id"] in ("tcg-baseline", "profile-balanced", "uml"):
            f = lambda k: r[k] / kvm[k]
            print(f"{r['id']} vs KVM: boot {f('boot_ms_median'):.1f}x, loop {f('guest_shell_loop_s_median'):.1f}x, "
                  f"sha {f('guest_sha256_16mb_s_median'):.1f}x, parallel {f('guest_parallel_loop_s_median'):.1f}x, syscalls {f('guest_syscalls_s_median'):.1f}x")


if __name__ == "__main__":
    main()
