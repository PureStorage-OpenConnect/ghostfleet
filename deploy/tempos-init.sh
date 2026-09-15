#!/bin/sh
# GhostFleet temp OS init (PID 1): minimal bring-up, then the agent.
# Runs from an Alpine-based initramfs — keep it boring and observable.

# The kernel starts PID 1 with no PATH; export one so the agent (and the
# fio / mkfs.xfs it execs) can be found. xfsprogs lives in /sbin.
export PATH=/usr/sbin:/usr/bin:/sbin:/bin

mount -t proc proc /proc
mount -t sysfs sys /sys
mount -t devtmpfs dev /dev 2>/dev/null
mount -t tmpfs tmpfs /tmp

echo "[ghostfleet-init] loading drivers"
modprobe vmxnet3    || echo "[ghostfleet-init] WARN: vmxnet3 failed"
modprobe vmw_pvscsi || echo "[ghostfleet-init] WARN: vmw_pvscsi failed"
modprobe sd_mod 2>/dev/null
modprobe xfs        || echo "[ghostfleet-init] WARN: xfs module failed"

echo "[ghostfleet-init] bringing up network (IPv6 SLAAC via RA)"
ip link set lo up
ip link set eth0 up || echo "[ghostfleet-init] WARN: no eth0"

# Give devtmpfs a moment to populate disk nodes before the agent scans them.
sleep 2

exec /agent
