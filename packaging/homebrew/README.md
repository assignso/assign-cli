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
