# Developing GhostFleet

This guide covers the two development loops and how to test a pull request
end-to-end:

1. **Laptop loop** — build, unit tests, controller + web UI against a local
   vSphere simulator. Fast, needs no hypervisor, but cannot boot VMs.
2. **Dev controller VM** — the full container stack built from a source
   checkout, attached to a real hypervisor and an isolated network. The only
   place where the PXE boot chain, the temp OS and the agent actually run.

CI (`.github/workflows/ci.yml`) runs `go vet`, `go test`, `go build`, the web
build, and a `docker build` of the **controller image only**. Anything that
touches the `netboot` or `tempos` images (iPXE, dnsmasq, the temp-OS kernel
and initramfs, the agent, or the base images of all three Dockerfiles) is
not exercised by CI and needs a live run on a dev VM before merging.

## 1. Laptop loop

Requirements: Go ≥ 1.26, Node ≥ 22, GNU make, and Docker if you want to
build images. A plain `make` lists every target with a one-line description;
the ones below are the laptop loop:

```sh
make build     # bin/controller, bin/agent, web/dist
make test      # go vet, go test, vue-tsc type-check
make run       # controller on :8080 serving web/dist, state in ./data
```

For UI work run the Vite dev server next to the controller; it proxies `/api`
and `/healthz` to `localhost:8080` and hot-reloads:

```sh
cd web && npm ci
make web-dev                          # http://localhost:5173
```

Without a vCenter, start the bundled simulator and point a connection at it
(`https://127.0.0.1:8989/sdk`, user `user`, password `pass`, "skip TLS
verification" on):

```sh
make vcsim
```

vcsim serves datacenter `DC0`, cluster `DC0_C0`, datastore `LocalDS_0` and
network `VM Network`. Connections, placement listing, profiles and the deploy
step (VM creation, tags, disks) all work against it. Nothing PXE-boots, so
fills never start and VMs never register — that is what the dev VM is for.

Unit tests use the fake hypervisor driver (`internal/hypervisor/fake`, tests
only — it is not registered in the controller binary) and govmomi's
simulator for the vSphere driver.

## 2. Dev controller VM

A dev VM is the controller VM from [INSTALL.md](INSTALL.md), except the
images are built from your checkout via `deploy/docker-compose.yml` instead
of pulled from GHCR via the release `docker-compose.yml`
(`deploy/docker-compose.dist.yml` in the tree).

### Sizing

The INSTALL minimum (2 vCPUs, 4 GiB) is enough to run, but a full build of
all three images takes about 10 minutes on 2 vCPUs — the iPXE compile in the
netboot image dominates. Give the VM more cores if you rebuild often.

### Prepare the VM

1. **OS and Docker.** Any recent Debian/Ubuntu. Install Docker Engine with
   the compose plugin from Docker's repository (the distro packages tend to
   lag), then let your user talk to the daemon and log in again:

   ```sh
   sudo usermod -aG docker "$USER"
   sudo apt install -y git make
   ```

   `make` is not part of a minimal Ubuntu install; every workflow below
   goes through it.

2. **Two NICs.** Management on one, the isolated portgroup on the other.
   Configure the isolated NIC to **not** act as a client on that network:
   no DHCP, no router-advertisement acceptance, link-local only. The
   netboot container advertises itself as the IPv6 router there; a host
   that accepts those RAs would configure a SLAAC address and default route
   from its own controller, and a DHCPv6 client would take a lease from the
   PXE pool. netplan example (`/etc/netplan/*.yaml`, then
   `sudo netplan apply`):

   ```yaml
   network:
     version: 2
     ethernets:
       ens34:
         match:
           macaddress: 00:50:56:xx:xx:xx
         set-name: ens34
         dhcp4: false
         dhcp6: false
         accept-ra: false
         link-local: [ipv6]
   ```

   The controller address `fd47:486f:7374::1/64` is added by the netboot
   container at start; do not configure it statically.

3. **Isolated portgroup reach.** Note which ESXi hosts carry the isolated
   portgroup. If it is a standard-switch portgroup on a single host, every
   deployment placement must pin that host; a distributed portgroup works
   cluster-wide.

4. **Clone.** The repository is public, so no credentials are needed to
   fetch branches or PR heads. As a contributor, clone **your fork** as
   `origin` — that is where your branches live and what the dev VM builds —
   and add the original repository as `upstream`, which is where pull
   requests are opened:

   ```sh
   git clone https://github.com/<you>/ghostfleet.git ~/ghostfleet
   cd ~/ghostfleet
   git remote add upstream https://github.com/PureStorage-OpenConnect/ghostfleet.git
   ```

   Maintainers without a fork clone upstream directly; then `origin` serves
   both roles.

5. **Configure.** Compose reads `.env` from the directory of the compose
   file, so it lives in `deploy/`:

   ```sh
   cd ~/ghostfleet/deploy
   cp env.example .env       # set GHOSTFLEET_ISOLATED_IF; a password is optional on a lab VM
   sh preflight.sh
   ```

### Build and start

```sh
cd ~/ghostfleet
make images      # build core, netboot, tempos from this checkout
make up          # start the stack
make status      # container state, temp OS result, running version
```

The version stamp baked into the binaries and images is `git describe
--tags --always --dirty` of the checkout (e.g. `v1.0.0-4-g7211efb`); `make`
computes it, and `-dirty` means the tree had uncommitted changes. To stamp
something else, pass `VERSION=` on the command line: `make images
VERSION=v1.1.0-rc1`. (Plain `docker compose -f deploy/docker-compose.yml`
works too, but then you must export `GHOSTFLEET_VERSION` yourself or the
stack reports `dev`.)

`make status` should show `temp OS artifacts installed` and the version of
the commit you built. Also check the isolated NIC:

```sh
make logs SVC=netboot                            # dnsmasq: DHCPv6 range, RA, TFTP on the isolated NIC
ip -6 addr show "$GHOSTFLEET_ISOLATED_IF"        # fd47:486f:7374::1/64 present
```

Open the UI on the management IP (port 80) or through a tunnel
(`ssh -L 8080:localhost:8080 user@dev-vm`, then `http://localhost:8080`).
Add the hypervisor connection, create a profile, and run one **deploy +
fill** with a small profile (two VMs, one 10 GiB data disk each takes a
couple of minutes). That run is your baseline: it proves the boot chain on
this VM/host/portgroup, so later failures are attributable to the change
under test, not the environment.

### Rebuild after a change

Rebuild only the image that owns the change, then recreate that service.
State (connections, profiles, deployments, run history) lives in the named
volume `state`, boot artifacts in `images`; both survive recreation and
`make down`. Only `docker compose -f deploy/docker-compose.yml down -v`
deletes them.

| You changed | Image | Rebuild + apply |
|---|---|---|
| `internal/`, `cmd/controller/`, `web/` | `core` | `make core` |
| `cmd/agent/`, `deploy/tempos-init.sh`, `deploy/Dockerfile.tempos` | `tempos` | `make tempos` |
| `deploy/embed.ipxe`, `deploy/dnsmasq.conf.tmpl`, `deploy/netboot-entrypoint.sh`, `deploy/Dockerfile.netboot` | `netboot` | `make netboot` |
| base images, `go.mod`, anything you are unsure about | all | `make images restart` |

Each target builds the image and recreates just that service. `tempos` is a
one-shot service: it must run again to copy the new kernel and initramfs
into the `images` volume, and `core` waits for it, so `make tempos` also
restarts `core`. Check with `make status`. A new agent reaches VMs the next
time they PXE-boot (VMs are powered off after a fill by default and boot
fresh for every run), so trigger a run or power-cycle the deployment after
the rebuild.

The build step aborts the target on failure, so a broken build never
recreates containers from stale images. If you script around `docker
compose` yourself, keep that property: don't pipe the build through `tail`
or `tee` without `set -o pipefail`, and check `/api/v1/version` afterwards.

### Getting your working copy onto the VM

- **Committed work (preferred):** push the branch to your fork, then on the
  VM `make deploy REF=<branch>` (see below). The version stamp is clean and
  reproducible.
- **Uncommitted work (quick):** rsync the tree from your laptop without
  `--delete`, excluding build outputs and local state:

  ```sh
  rsync -az --exclude node_modules --exclude web/dist --exclude bin --exclude data \
        ./ user@dev-vm:ghostfleet/
  ```

  The VM's checkout then shows as `-dirty`; that is expected.

## 3. Testing a pull request

1. **Baseline first.** The dev VM should be on `main` with the last deploy +
   fill green (see above). Don't test a PR on a VM that never booted a
   ghost.
2. **Check out the PR head, rebuild everything, recreate the stack:**

   ```sh
   cd ~/ghostfleet
   make deploy REF=pr/<N>
   ```

   `REF` may also be a branch or a tag; those are fetched from `origin`,
   your fork. A `pr/<N>` head is fetched anonymously from `upstream` when
   that remote exists, otherwise from `origin`, because GitHub publishes
   pull request heads only on the repository the PR targets. `REMOTE=`
   overrides either choice. The head is checked out as local branch
   `pr-<N>`, all three images are rebuilt — a PR may touch any layer, and a
   base-image or module bump touches all three — and the containers are
   recreated.
   `make deploy` refuses to run over uncommitted changes to tracked files,
   so nothing on the VM gets lost. The `status` output at the end must show
   `temp OS artifacts installed` and a version ending in the PR head's
   short SHA.

   Keep the `state` volume — testing on top of the state `main` left
   behind is exactly the upgrade path users will take.

3. **Sanity-check** the UI loads and `make logs SVC=core` is quiet.

4. **Run the checks the change calls for.** In rough order of cost:

   - *Controller, API, UI* (`internal/`, `web/`): UI loads and the changed
     screens work; connection **validate**; create/edit a profile;
     `make logs SVC=core` stays free of errors and warnings.
   - *Hypervisor driver / govmomi bump* (`internal/hypervisor/vsphere`,
     `go.mod`): validate the connection, list placement, **deploy** a new
     deployment (VM creation, disk attach, tag), power on/off, scale the
     spec up, **tear down**. Look for the VMs and tag in vCenter.
   - *Boot chain* (`deploy/`, `cmd/agent/`, base images): deploy and
     **fill** a fresh deployment. Watch `make logs SVC=netboot` for the
     DHCPv6 solicit/reply and the TFTP transfer of `snponly.efi`, then the
     per-VM serial console in the UI (deployment → VM → console) for iPXE
     fetching `vmlinuz`/`initramfs.gz`, the temp OS bringing up SLAAC, and
     the agent registering. The fill must complete with plausible
     throughput, and the VMs must power off afterwards (default
     `afterFill: shutdown`).
   - *Data path* (`internal/datagen`, agent): after the fill run an
     **incremental** run and check the reported change/growth; run a
     **verify** if the PR touches hashing or seeding.
   - *Store / SQLite bump* (`internal/store`, `go.mod`): the stack must come
     up on the existing `state` volume (migrations), and existing
     deployments must still list, run and tear down.

5. **Record and merge.** Note what you ran and the outcome in a PR comment
   (version string, deployment size, which runs), then merge.

6. **Back to `main`.**

   ```sh
   make deploy REF=main          # origin/main — sync your fork's main first if it lags upstream
   git branch -D pr-<N>          # optional tidy-up
   ```

   If the PR added a store migration (`internal/store/migrations/`) and you
   roll back to a `main` without it, the older controller starts anyway —
   it only applies migrations it knows and skips a higher `user_version` —
   but its queries can fail against the changed schema. In that case tear
   the deployments down from the UI first, then `make down && docker
   compose -f deploy/docker-compose.yml down -v` and rebuild the baseline
   (`make images up`).

## 4. Checking a release the way users install it

To test the published images rather than a source build, stop the dev
stack and run the pull-only stack from a separate directory, pinned to the
tag (both stacks publish ports 80/8080, so only one runs at a time):

```sh
make -C ~/ghostfleet down                                         # keeps volumes
mkdir -p ~/ghostfleet-release && cd ~/ghostfleet-release
REL=https://github.com/PureStorage-OpenConnect/ghostfleet/releases/latest/download
curl -fsSL -O $REL/docker-compose.yml -O $REL/env.example -O $REL/preflight.sh
cp env.example .env && echo GHOSTFLEET_VERSION=v1.0.0 >> .env   # plus GHOSTFLEET_ISOLATED_IF
docker compose up -d
```

This uses its own volumes (project name `ghostfleet-release`), so the dev
stack's state is untouched.
