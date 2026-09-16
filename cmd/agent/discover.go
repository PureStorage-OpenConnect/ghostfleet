//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
)

// runDiscovery is the agent's life as an unknown machine: inspect every
// attached disk (read-only) for GhostFleet identity markers, report the
// findings, then heartbeat until the operator decides. Once the VM is
// adopted the controller answers "reboot" — the fresh PXE boot then matches
// a managed VM and the agent starts its normal life. Never returns.
func runDiscovery(client *httpClient) {
	report := inspectDisks()
	payload, _ := json.Marshal(report)
	logf("discovery: %s", payload)

	client.postReliable("/agent/v1/discovery/report", map[string]any{
		"token": client.token, "disks": report,
	})

	for {
		time.Sleep(10 * time.Second)
		status, body, err := client.post("/agent/v1/discovery/heartbeat", map[string]string{"token": client.token})
		if err != nil || status != 200 {
			logf("discovery heartbeat failed (status %d, err %v)", status, err)
			continue
		}
		var resp struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err == nil && resp.Action == "reboot" {
			logf("adopted — rebooting into managed life")
			rebootVM()
		}
	}
}

// inspectDisks mounts every attached disk read-only and reads the GhostFleet
// markers, if any. A disk that doesn't mount as the expected filesystem is
// still reported (device + size), just without identity. Used by the
// discovery boot and, since the report travels with register, by every
// managed boot too — so the disks are left unmounted for the fill that may
// follow.
func inspectDisks() []datagen.DiscoveryDisk {
	names, err := allBlockDevices()
	if err != nil {
		logf("discovery: listing disks: %v", err)
		return nil
	}
	var out []datagen.DiscoveryDisk
	for i, name := range names {
		disk := datagen.DiscoveryDisk{Device: name, SizeGiB: blockDeviceGiB(name)}
		dev, mnt := "/dev/"+name, fmt.Sprintf("/mnt/discover%d", i)
		// Read-only keeps the inspection strictly non-destructive (XFS may
		// still replay its log onto the device if the backup was
		// crash-consistent — that is recovery, not modification of the data).
		if err := mountFSReadOnly(dev, mnt, "xfs"); err != nil {
			logf("discovery: %s: no mountable xfs (%v)", dev, err)
			out = append(out, disk)
			continue
		}
		disk.Filesystem = "xfs"

		if data, err := os.ReadFile(filepath.Join(mnt, datagen.IdentityFile)); err == nil {
			var id datagen.IdentityMarker
			if err := json.Unmarshal(data, &id); err == nil {
				disk.Identity = &id
			}
		}
		if data, err := os.ReadFile(filepath.Join(mnt, runMarkerFile)); err == nil {
			fields := strings.Fields(string(data))
			if len(fields) >= 1 {
				disk.RunID = fields[0]
			}
			if len(fields) >= 2 {
				disk.RunBytes, _ = strconv.ParseInt(fields[1], 10, 64)
			}
		}
		if m, err := loadManifest(mnt); err == nil {
			disk.Manifest = true
			disk.FileCount = len(m.Files)
		}
		// A plain unmount first: a fill may mkfs this device right after,
		// which must not find a lazily-detached mount still on it.
		if err := syscall.Unmount(mnt, 0); err != nil {
			_ = syscall.Unmount(mnt, syscall.MNT_DETACH)
		}
		out = append(out, disk)
	}
	return out
}

// allBlockDevices returns every attached disk (sd*/vd*), sorted.
func allBlockDevices() ([]string, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}
	var disks []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "vd") {
			disks = append(disks, name)
		}
	}
	sort.Strings(disks)
	return disks, nil
}

// blockDeviceGiB reads a disk's size from sysfs (512-byte sectors).
func blockDeviceGiB(name string) int {
	data, err := os.ReadFile("/sys/block/" + name + "/size")
	if err != nil {
		return 0
	}
	sectors, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return int(sectors * 512 >> 30)
}

// rebootVM restarts the machine so it PXE-boots with its (now managed) MAC.
func rebootVM() {
	syscall.Sync()
	if err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART); err != nil {
		logf("reboot failed: %v", err)
		sleepForever()
	}
}
