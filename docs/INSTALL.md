# Installing a GhostFleet controller

This guide takes you from an empty VM to a running GhostFleet controller
using prebuilt container images — no source checkout, no build toolchain.

## What you need

**A controller VM** (any recent Linux; Debian/Ubuntu/Alpine all fine):

- 2 vCPUs, 4 GiB RAM, ~20 GiB disk
- Docker Engine with the compose plugin (`docker compose version` works)
- **Two network interfaces:**
  1. **Management** — where you reach the web UI (port 80/8080) and where the
     controller reaches the hypervisor API (e.g. vCenter, port 443).
  2. **Isolated** — attached to an isolated portgroup/VLAN that the generated
     VMs will share. GhostFleet runs its own IPv6-only PXE boot services
     there; nothing else should live on that network. No IP configuration
     needed on this NIC — just bring the link up.

**On the hypervisor** (vSphere first): a service account for the controller —
see [VSPHERE-SETUP.md](VSPHERE-SETUP.md) — and the isolated portgroup, made
available to every host the generated VMs may run on.

The architecture diagram at the top of [ARCHITECTURE.md](ARCHITECTURE.md)
shows how the two networks, the controller containers, the hypervisor and the
generated VMs fit together.

## Install

All commands run on the controller VM, in a directory of your choice
(e.g. `~/ghostfleet`).

**1. Get the release assets** — `docker-compose.yml`, `env.example`, and
`preflight.sh` are attached to every
[GitHub release](https://github.com/PureStorage-OpenConnect/ghostfleet/releases).
The repository and its container images are public, so no GitHub account and
no registry login are needed:

```sh
REL=https://github.com/PureStorage-OpenConnect/ghostfleet/releases/latest/download
curl -fsSL -O $REL/docker-compose.yml -O $REL/env.example -O $REL/preflight.sh
```

With the [`gh` CLI](https://cli.github.com/) instead:
`gh release download -R PureStorage-OpenConnect/ghostfleet -p docker-compose.yml -p env.example -p preflight.sh`.
Or have someone send you the three small files — they are plain text.

**2. Configure:**

```sh
cp env.example .env
vi .env    # set GHOSTFLEET_ISOLATED_IF; set a password
```

`GHOSTFLEET_ISOLATED_IF` is the only required setting: the name of the NIC on
the isolated network (`ip -br link` — it's the one without your management
IP).

**3. Preflight (optional, recommended):**

```sh
sh preflight.sh
```

Checks Docker, the isolated interface (exists, up, IPv6 enabled), and that
ports 80/8080 are free.

**4. Start:**

```sh
docker compose up -d
```

Three containers start: `core` (UI/API/boot endpoints), `netboot`
(RA/DHCPv6/TFTP on the isolated interface), and a one-shot `tempos` that
installs the PXE boot artifacts. Open `http://<controller-vm>/` — you're
looking at the dashboard. Continue with the README's workflow: add a
hypervisor connection, create a profile, deploy.

## Upgrading

State (connections, profiles, deployments, run history) lives in a named
Docker volume and survives upgrades:

```sh
docker compose pull && docker compose up -d
```

If you pinned `GHOSTFLEET_VERSION` in `.env` (recommended), bump the pin
first. Check the running version at `http://<controller-vm>/api/v1/version`.

## Uninstalling

```sh
docker compose down        # stop; keeps state
docker compose down -v     # stop AND delete all state — irreversible
```

Tear down deployments from the UI first if you want the generated VMs
removed from the hypervisor.

## Troubleshooting

- **Generated VMs never register** — check the netboot service sees DHCP
  requests (`docker compose logs netboot`), then the per-VM serial console in
  the UI (deployment → VM → console), which shows the full boot including PXE
  and agent output. A fill power-cycles wedged VMs automatically and names
  never-registered VMs in the run error.
  If the netboot log shows requests arriving but answers
  `no address range available for DHCPv6 request`, the isolated interface is
  missing the controller address `fd47:486f:7374::1/64`, which netboot
  assigns at container start — look for the assignment error in its log, or
  add the address manually.
- **`netboot` restarting** — almost always `GHOSTFLEET_ISOLATED_IF` naming a
  non-existent interface. `ip -br link` lists the real names.
- **No TLS** — deliberate for a lab tool on a trusted management network; put
  nginx/Caddy/Traefik in front if you need HTTPS.
