package config

// These variables are set via ldflags during build time
var (
	Version  = "dev"     // git describe --tag --abbrev=0
	Revision = "unknown" // git rev-list -1 HEAD
	Build    = "dev"     // git describe --tags
)
