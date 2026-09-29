# Homebrew release and production gates

The first-party distribution is a cask generated from checksummed GitHub
release archives in `assignso/homebrew-tap`. Current Homebrew resolves this
uniquely named cask through `brew install assignso/tap/assign`; `--cask` remains
an equivalent explicit spelling.

Preview `v0.1.0-preview.1` was published on 2026-08-21 with all six configured
platform archives, SHA-256 checksums, and a generated `Casks/assign.rb`.
`brew install assignso/tap/assign` was verified on Apple silicon. The install
succeeds, but macOS blocks the executable because it is not Developer ID signed
or notarized. The scoped `HOMEBREW_TAP_GITHUB_TOKEN` Actions secret was
configured on 2026-08-21, so subsequent tags can publish through the release
workflow.

Before publishing the next release:

1. add Developer ID signing and Apple notarization for macOS artifacts;
2. verify execution on both macOS architectures without a Gatekeeper bypass;
3. test upgrade, rollback, and uninstall from a clean Homebrew prefix.

Production release still requires macOS signing/notarization, artifact
provenance, revocation ownership, and a supported rollback procedure. Preview
documentation must not imply those production qualifications are complete. Do
not add an automatic quarantine-removal hook as a substitute for signing.

## Official catalog delivery

The accepted launch requirement is `brew install assign` on a fresh Homebrew
installation, without adding an Assign tap. The existing generated cask and
`brew install assignso/tap/assign` preview do not satisfy that requirement.

The CLI is approved for open-source publication under
[ADR 0116](../../../architecture/decisions/0116-open-source-the-cli-and-distribute-compiled-packages.md).
The target is an `assign` formula in `homebrew/core`, with precompiled bottles
for supported users. License/source preparation and Homebrew acceptance remain
pending; this supersedes the earlier cask-only submission plan. Follow the
[canonical tap-free release gate](../../../architecture/integrations/cli.md#tap-free-homebrew-release-gate)
and [BREW-01–04 delivery checklist](../../../architecture/roadmap/developer-cli.md#tap-free-homebrew-delivery--2026-09-29)
for artifact qualification, upstream submission, maintenance ownership and clean
installation evidence on each claimed platform. The tap publication workflow
cannot itself make the package available in Homebrew's default catalog.

Keep user instructions on the verified installation channel until upstream
acceptance and fresh-install verification are complete. No official-catalog
submission or successful tap-free install is recorded by this update.

Package review must satisfy the shared
[CLI packaging review criteria PKG-01–11](../../../architecture/integrations/cli.md#cli-packaging-review-criteria)
before qualification. The checklist covers source/license completeness, clean
builds, compiled installation, package lifecycle, Homebrew review and retained
evidence; APT-specific criteria apply to the separate Linux package channels.
Criteria being documented does not mean the review has passed.
