// Package buildinfo exposes version information stamped at build time.
package buildinfo

// Version is overridden at build time via
//
//	-ldflags "-X github.com/PureStorage-OpenConnect/ghostfleet/internal/buildinfo.Version=<version>"
var Version = "dev"
