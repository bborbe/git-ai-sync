package version

// Version is the version string printed by --version. It may be overridden at
// build time with -ldflags "-X github.com/bborbe/git-ai-sync/internal/version.Version=<v>".
var Version = "dev"
