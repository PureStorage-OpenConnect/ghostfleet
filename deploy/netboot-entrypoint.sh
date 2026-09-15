#!/bin/sh
# Render dnsmasq.conf for the isolated interface and run dnsmasq in the
# foreground. GHOSTFLEET_ISOLATED_IF must name the host interface that is
# connected to the isolated portgroup (e.g. ens34).
set -e

IFACE="${GHOSTFLEET_ISOLATED_IF:?set GHOSTFLEET_ISOLATED_IF to the isolated interface (e.g. ens34)}"
sed "s/__IFACE__/${IFACE}/g" /etc/dnsmasq.conf.tmpl > /etc/dnsmasq.conf

# The controller's fixed ULA on the isolated interface. dnsmasq refuses to
# serve a DHCPv6 range ("no address range available") unless the interface
# holds an address in that prefix, and the whole boot chain targets ::1 —
# so assign it here (idempotent; NET_ADMIN + host network) instead of
# depending on a manual host netplan step that is easy to miss on a new
# controller VM.
ip -6 addr replace fd47:486f:7374::1/64 dev "${IFACE}"

echo "netboot: dnsmasq on ${IFACE}, serving snponly.efi via TFTP on [fd47:486f:7374::1]"
exec dnsmasq --keep-in-foreground --conf-file=/etc/dnsmasq.conf
