//go:build !linux

package main

import "github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"

// runFill is only implemented for Linux (the temp OS). This stub lets the
// agent package build on dev/CI hosts (darwin) for vetting and testing.
func runFill(_ *httpClient, _ *datagen.WorkOrder) {
	logf("data generation is only supported on the Linux temp OS")
}

// runDiscovery is only implemented for Linux (the temp OS).
func runDiscovery(_ *httpClient) {
	logf("discovery is only supported on the Linux temp OS")
	sleepForever()
}

// inspectDisks is only implemented for Linux (the temp OS).
func inspectDisks() []datagen.DiscoveryDisk { return nil }
