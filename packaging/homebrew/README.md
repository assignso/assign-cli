# Homebrew release gate

The intended first-party distribution is a binary cask generated from signed,
checksummed GitHub release archives into `assignso/homebrew-tap`.

Before enabling GoReleaser's `homebrew_casks` publisher:

1. accept which Assign CLI binaries are public despite the closed source tree;
2. create and register `assignso/homebrew-tap` in the repository map;
3. configure a separate token with content-write access to that tap;
4. add macOS signing/notarization and artifact provenance verification;
5. release a stable immutable version and verify both macOS architectures;
6. test install, upgrade, rollback, and uninstall from a clean Homebrew prefix.

Until those gates pass, the repository can produce local snapshot archives but
must not claim that `brew install assign` is available.
