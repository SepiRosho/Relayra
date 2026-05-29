package buildinfo

var (
	// Version is set via ldflags at build time.
	Version = "0.1.5"

	// BuildDate is set via ldflags at build time when available.
	BuildDate = "unknown"
)
