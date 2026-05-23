package version

// Set at build time via -ldflags "-X github.com/samansartipi/curio/internal/version.Version=..."
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func String() string {
	return Version + " (" + Commit + ", " + Date + ")"
}
