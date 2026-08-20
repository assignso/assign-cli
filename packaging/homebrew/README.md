# Homebrew release and production gates

The first-party distribution is a cask generated from checksummed GitHub
release archives in `assignso/homebrew-tap`. Current Homebrew resolves this
uniquely named cask through `brew install assignso/tap/assign`; `--cask` remains
an equivalent explicit spelling.

Before publishing the first preview:

1. make `assignso/homebrew-tap` publicly readable so Homebrew can fetch releases;
2. configure the `HOMEBREW_TAP_GITHUB_TOKEN` secret with contents write access;
3. publish an immutable preview tag and verify both macOS architectures;
4. test install, upgrade, rollback, and uninstall from a clean Homebrew prefix.

Production release still requires macOS signing/notarization, artifact
provenance, revocation ownership, and a supported rollback procedure. Preview
documentation must not imply those production qualifications are complete, and
macOS may block an unnotarized preview binary until the user explicitly approves
it in Privacy & Security settings.
