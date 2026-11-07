## What and why

<!-- One paragraph. Link the issue if there is one. -->

## Checklist

- [ ] `make lint test` pass locally
- [ ] Detection changes come with a fixture (`testdata/gen_fixtures.py`)
- [ ] System changes are `ops.Op`s: described exactly, idempotent, reversible, covered by a temp-root test
- [ ] `--dry-run` and `--json` behave for any new command
- [ ] Performance claims in docs come from `kvmx bench` output
- [ ] Software emulation is never presented as hardware KVM
