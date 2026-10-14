## Summary

<!-- What changes, and why. One short paragraph is usually enough. -->

## Linked issue

<!--
Use "Closes #N" only when this PR fully resolves the issue; use "Refs #N" when
work remains. For more than one issue, repeat the keyword before each number
(one per line), since "Closes #1, #2" closes only #1.
-->

Closes #

## Test plan

<!-- How you verified the change. Note anything you tested by hand on Unraid. -->

## Gates

<!-- Check what you ran. Leave unchecked (and say why) anything that does not apply. -->

- [ ] `make test` passes
- [ ] `make lint` passes (lints the host GOOS and linux)
- [ ] `make php-lint` passes (if PHP or `.page` files changed)
- [ ] `make vulncheck` is clean
- [ ] `bash scripts/pre-push-gate.sh` passes
- [ ] Docs updated if behavior or configuration changed
- [ ] No secrets, API keys, or host details in the diff
