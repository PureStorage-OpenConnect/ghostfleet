#!/bin/sh
# GhostFleet controller-VM preflight: checks the host before the first
# `docker compose up`. Run as root (or a docker-group user) on the
# controller VM:
#
#   sh preflight.sh            # reads GHOSTFLEET_ISOLATED_IF from ./.env
#   sh preflight.sh ens34      # or name the isolated interface explicitly
#
# Exits non-zero if a hard requirement fails.

fails=0
pass() { printf 'PASS  %s\n' "$1"; }
warn() { printf 'WARN  %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; fails=$((fails + 1)); }

# --- Docker ---
if command -v docker >/dev/null 2>&1; then
    if docker info >/dev/null 2>&1; then
        pass "docker daemon is running"
    else
        fail "docker is installed but the daemon is not reachable (is it running? are you in the docker group?)"
    fi
else
    fail "docker is not installed"
fi
if docker compose version >/dev/null 2>&1; then
    pass "docker compose plugin available"
else
    fail "docker compose plugin missing (install docker-compose-plugin)"
fi

# --- Isolated interface ---
IFACE="${1:-}"
if [ -z "$IFACE" ] && [ -f .env ]; then
    IFACE=$(sed -n 's/^GHOSTFLEET_ISOLATED_IF=//p' .env | tail -1)
fi
IFACE="${IFACE:-${GHOSTFLEET_ISOLATED_IF:-}}"
if [ -z "$IFACE" ]; then
    fail "no isolated interface named (pass it as an argument or set GHOSTFLEET_ISOLATED_IF in .env)"
else
    if ip link show "$IFACE" >/dev/null 2>&1; then
        pass "isolated interface $IFACE exists"
        case "$(cat /sys/class/net/"$IFACE"/operstate 2>/dev/null)" in
            up) pass "$IFACE is up" ;;
            *)  warn "$IFACE is not up — check the VM's second NIC is connected to the isolated portgroup" ;;
        esac
        if [ "$(sysctl -n net.ipv6.conf."$IFACE".disable_ipv6 2>/dev/null)" = "1" ] ||
           [ "$(sysctl -n net.ipv6.conf.all.disable_ipv6 2>/dev/null)" = "1" ]; then
            fail "IPv6 is disabled on $IFACE — the boot chain is IPv6-only (check sysctl net.ipv6.conf.*.disable_ipv6)"
        else
            pass "IPv6 enabled on $IFACE"
        fi
        if ip -4 addr show "$IFACE" 2>/dev/null | grep -q 'inet '; then
            warn "$IFACE has an IPv4 address — expected for some setups, but the isolated network should normally carry no other services"
        fi
    else
        fail "interface $IFACE does not exist (ip -br link lists the available ones)"
    fi
fi

# --- Ports ---
for port in 80 8080; do
    if ss -ltnH 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$port\$"; then
        fail "TCP port $port is already in use — the controller publishes the UI/API there"
    else
        pass "TCP port $port free"
    fi
done

echo
if [ "$fails" -gt 0 ]; then
    echo "$fails check(s) failed — fix the FAIL lines before 'docker compose up -d'."
    exit 1
fi
echo "All hard checks passed. Continue with docker-compose.yml + .env (see docs/INSTALL.md)."
