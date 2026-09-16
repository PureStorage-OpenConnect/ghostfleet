<p align="center"><img src="docs/logo.svg" alt="GhostFleet" width="380"></p>

# GhostFleet

**Fleets of ghost VMs that exist only to make data.**

GhostFleet is a calibrated **synthetic data generator for virtual infrastructure**. It
deploys fleets of lightweight, OS-less PXE-booted VMs on VMware vSphere and other
hypervisors (Nutanix AHV, Proxmox VE, Hyper-V, standalone ESXi) and fills their virtual
disks with data whose **size, compressibility, deduplicability (within and across VMs),
change rate and growth rate** are precisely configurable — realistic, repeatable and
fully controllable.

Use it to put **calibrated load and data on storage**:

- validate a storage array's **dedup and compression** ratios against known inputs,
- benchmark **backup software, repositories and dedup appliances** with repeatable workloads,
- exercise **capacity and efficiency planning**, or generate steady **change/growth churn**
  for any storage or data-protection testing.

The generated VMs are literal ghosts: no installed operating system, just precisely
calibrated data on their disks, like a
[reserve fleet](https://en.wikipedia.org/wiki/Reserve_fleet) kept ready for exercises.

## How it works (high level)

```
                 ┌────────────────────────────────────────────┐
 Management ─────┤  Controller VM (manually deployed Linux)   │
 network         │  ┌──────────────────────────────────────┐  │
 (SSH, Web UI)   │  │ Containerized controller stack:      │  │
                 │  │  - Web UI + REST API                 │  │
                 │  │  - Hypervisor plugins (vSphere, ...) │  │
                 │  │  - Profile / deployment database     │  │
                 │  │  - DHCPv6 + PXE/HTTP boot services   │  │
                 │  └──────────────────────────────────────┘  │
                 └───────────────┬────────────────────────────┘
                                 │ Isolated network (IPv6-only)
                 ┌───────────────┴────────────────────────────┐
                 │  Generated source VMs (PXE-booted temp OS) │
                 │  agent fills / mutates disks per profile   │
                 └────────────────────────────────────────────┘
```

(A rendered end-to-end diagram — operator, controller containers, hypervisor,
boot chain — is at the top of [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).)

1. Deploy the controller VM manually, attach it to a management network and an
   isolated network, start the containerized controller stack.
2. In the web UI, configure a hypervisor connection (e.g. vCenter) and a
   **source data profile** (VM count, naming scheme, disks, sizes, data
   characteristics, tag, ...).
3. **Deploy** the profile: the controller creates the VMs via the hypervisor
   API, attaches them to the isolated network, and PXE-boots them into a
   temporary in-memory OS that runs the data generation agent.
4. The agents perform the **initial data fill** and report live progress and
   throughput statistics to the controller.
5. Run your backup (out of scope), then trigger an **incremental run**: agents
   apply the configured change rate and growth rate to the existing data.
6. Scale the profile up at any time (more VMs / disks / data), or tear down the
   whole deployment while keeping the (versioned) profile.

## Documentation

| Document | Content |
|---|---|
| [docs/INSTALL.md](docs/INSTALL.md) | **Deploy a controller from prebuilt images** (docker compose, no source checkout) |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | **Develop and test**: laptop loop with vcsim, dev controller VM built from source, how to test a PR |
| [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md) | Consolidated functional & non-functional requirements |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Component architecture, network design, boot chain, data generation model |
| [docs/TECH-STACK.md](docs/TECH-STACK.md) | Chosen technology stack, with alternatives and rationale |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Implemented capabilities and planned enhancements |
| [docs/VSPHERE-SETUP.md](docs/VSPHERE-SETUP.md) | Least-privilege vSphere service account (custom role, sandbox objects, govc script) |
| [docs/DESIGN-DECISIONS.md](docs/DESIGN-DECISIONS.md) | Design decisions and their rationale |

## Development

Requirements: Go ≥ 1.26, Node ≥ 22 (for the web UI), GNU make, Docker (for the
container stack). `make` alone lists all targets; the everyday ones:

```sh
make build        # build controller + agent binaries into ./bin and the web UI
make test         # run Go tests (and web type-check)
make run          # run the controller locally on :8080
make images up    # build the container stack from source and start it (dev VM)
make deploy REF=pr/6   # check out a branch/tag/PR, rebuild, recreate — for testing
```

See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for the full development setup:
running against a local vSphere simulator, building the container stack from
source on a dev controller VM, and testing pull requests end-to-end.

## Versioning

GhostFleet follows [Semantic Versioning 2.0.0](https://semver.org/). Releases are
annotated git tags of the form `vMAJOR.MINOR.PATCH`; the version is stamped into
binaries and container images at build time via `git describe` (local builds
show `dev`).

`1.0.0` is the first stable release: the profile format and the REST API are
considered stable, so breaking changes to either bump the **major** version.
**Minor** releases add backward-compatible features; **patch** releases are fixes
only. See [CHANGELOG.md](CHANGELOG.md) for release notes.

## Status

**Stable and lab-tested.** GhostFleet is feature-complete for its initial scope
and hardened by real use on VMware vSphere 8.0.3, driving repeated fill and
incremental cycles against a live cluster.

Implemented capabilities:

- **vSphere plugin** — placement picker (cluster / host / datastore / folder /
  resource pool), deploy / scale-up / teardown with reconciliation, tag
  handling, and an owner-aware cross-deployment conflict flow (adopt / clean /
  abort).
- **IPv6 PXE boot chain** — dnsmasq (DHCPv6 + RA + TFTP) + iPXE + an in-memory
  Alpine temp OS that runs the data-generation agent.
- **Calibrated data generation** — fio-driven XFS fills with configurable size,
  compressibility, and within-/cross-VM deduplicability.
- **True incremental runs** — in-place, CBT-visible change and growth with no
  re-mkfs, plus throughput history and a statistics dashboard.
- **Restore validation** — disks carry identity markers and per-file SHA-256
  manifests; restored ghost VMs are auto-discovered at PXE boot, adoptable
  (in place, or into a side deployment), and a verify run proves the restore
  round-tripped the data intact.
- **Operational hardening** — idempotent deploys, run/job cancel, API keys,
  profile import/export, restart resilience, agent retry/resume, and fill boot
  resilience (wedged VMs are power-cycled; unbootable VMs fail alone).
- **Distribution** — prebuilt container images on GHCR and a pull-only compose
  stack; see the [install guide](docs/INSTALL.md).

Planned enhancements are tracked in [docs/ROADMAP.md](docs/ROADMAP.md).

To run the controller with a password: `GHOSTFLEET_PASSWORD=... ./bin/controller`.
For programmatic/automation access, set one or more static API keys with
`GHOSTFLEET_API_KEYS=key1,key2`; pass a key as `Authorization: Bearer <key>` or
`X-API-Key: <key>` (or `?apikey=<key>`, which is convenient but leaks into
logs — prefer a header). Configuring a password and/or API keys turns the API
gate on; with neither, all endpoints are open. The session cookie is marked
`Secure` automatically when a request arrives over TLS or a reverse proxy sets
`X-Forwarded-Proto: https`; force it with `GHOSTFLEET_SECURE_COOKIES=on|off`
if your proxy does not send that header. Every deployment action also
has a copy-pastable URL under its **API** button in the UI; see
[examples/Invoke-GhostfleetIncremental.ps1](examples/Invoke-GhostfleetIncremental.ps1)
for triggering an incremental run from a backup-job post script. State lives in
`./data` (override with `-data`). The API is described by an OpenAPI spec served
at `/api/v1/openapi.yaml` (also at the conventional `/openapi.yaml`); the
controller advertises it via an [RFC 8631](https://www.rfc-editor.org/rfc/rfc8631)
`service-desc` `Link` header and an [`/llms.txt`](https://llmstxt.org) pointer, so
tools and agents can discover it from the base URL. For development without a
vCenter,
`go run ./tools/vcsim` starts a local vSphere API simulator
(`https://127.0.0.1:8989/sdk`, user/pass, skip TLS verification).

## Support

GhostFleet is maintained on a **best-effort, community-supported basis**. It is
open-source software provided in the hope that it is useful, with **no warranty
and no guaranteed support or response time** (see the disclaimer of warranty in
the [Apache-2.0](LICENSE) license).

- **Bugs, questions, feature requests:** please open a
  [GitHub issue](https://github.com/PureStorage-OpenConnect/ghostfleet/issues). These are
  triaged as time permits.
- **Contributions** are welcome via pull request.

## Maintainers

| Name | GitHub | Role |
|---|---|---|
| Stefan Zimmermann | [@StefanZ8n](https://github.com/StefanZ8n) | Creator and currently the only active maintainer |

Questions, ideas and offers to participate are welcome: open a
[GitHub issue](https://github.com/PureStorage-OpenConnect/ghostfleet/issues) or
mention `@StefanZ8n` on an issue or pull request. If you would like to help
maintain GhostFleet, say so in an issue — reviewed contributions are the
natural path in (see [CONTRIBUTING.md](CONTRIBUTING.md)).

## Authorship

GhostFleet was created by Stefan Zimmermann, with
[Claude](https://www.anthropic.com/claude) (Anthropic) as an AI pair-programming
collaborator throughout its design and implementation.

## Contributing & security

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). To report a
security issue, see [SECURITY.md](SECURITY.md).

## License

Licensed under the [Apache License, Version 2.0](LICENSE).

Copyright 2026 Everpure, Inc.

GhostFleet bundles and depends on third-party open-source software, each under
its own license; see [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).
