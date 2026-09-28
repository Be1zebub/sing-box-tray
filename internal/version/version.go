package version

// Version identifies the running build. It is overridden at build time via
// -ldflags "-X github.com/Be1zebub/sing-box-tray/internal/version.Version=vX.Y.Z"
// (see scripts/build.sh, scripts/build.ps1, and .github/workflows/release.yml).
// "dev" is only what you get from a raw go build that skipped the build
// script. scripts/build.ps1, scripts/build.sh, and the Makefile pass git
// describe (or $VERSION, which the release workflow sets to the tag).
var Version = "dev"
