# Changelog

All notable changes to GhostFleet are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
project follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Agents report what their data disks hold when they register after boot
  (the same read-only inspection discovery uses), and the controller keeps a
  per-VM data state — filled, partial or blank — shown in the VM list. A
  deployment now counts as filled, which unlocks incremental and verify, when
  every VM reports filled disks and not only when this controller ran the
  initial fill itself. A fleet whose history the controller never saw (a
  re-installed controller, or existing VMs adopted on deploy) recovers by
  simply powering it on; no re-fill needed. Agents report only from the temp
  OS image of this release onward.

## [1.1.0] — 2026-09-16

### Added

- The session cookie carries the `Secure` attribute when the request arrived
  over TLS or a reverse proxy sent `X-Forwarded-Proto: https`; the new
  `GHOSTFLEET_SECURE_COOKIES=on|off` forces it either way. On plain HTTP it
  stays off, as before, so browsers on the trusted network keep working.

### Fixed

- A failed fill/incremental/verify run was marked finished before the post-run
  shutdown had powered the fleet off, so the UI, a schedule, or an immediately
  following run could see a "failed" run with VMs still on. The shutdown now
  completes first, as it already did for successful runs.
- The web UI's SPA fallback checked for a file by joining the raw request path
  onto the dist directory, so a dotted path could tell whether a file outside
  the dist directory exists (different status codes; the file itself was never
  served). The check now goes through the same root-confined `http.Dir` as the
  file server.
- A fill/incremental/verify run started within 30 s of the previous run's
  post-run shutdown booted nothing: the boot pass trusted the still-fresh agent
  heartbeats and skipped the powered-off VMs, leaving the run "running" until
  the boot watchdog power-cycled the fleet five minutes later. The boot pass now
  goes by the hypervisor's actual power state, and the post-run shutdown clears
  agent liveness so a powered-off VM never shows an online agent.

### Development

- Development guide ([docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)) and a
  make-based workflow for building, running the dev stack, and deploying a PR
  or branch to a test host.
- Dependabot version updates for Go modules, npm, GitHub Actions and base
  images, plus a CODEOWNERS file; the CI workflow's token is read-only and the
  Go tests run with `-race`.

## [1.0.0] — 2026-09-15

First stable, public release. From this point the profile format and the REST
API are considered stable; breaking changes to either will bump the major
version.

### Added

- Calibrated synthetic data generation for virtual infrastructure: fleets of
  OS-less, PXE-booted VMs whose disks are filled with data of configurable size,
  compressibility and within-/cross-VM deduplicability.
- VMware vSphere plugin: placement picker (cluster / host / datastore / folder /
  resource pool), deploy / scale-up / teardown with desired-vs-actual
  reconciliation, tag handling, and an owner-aware cross-deployment conflict flow
  (adopt / clean / abort).
- IPv6 PXE boot chain: dnsmasq (DHCPv6 + RA + TFTP) + iPXE + an in-memory Alpine
  temp OS running the data-generation agent.
- True incremental runs: in-place, CBT-visible change and growth with no
  re-mkfs, plus run history, throughput statistics and a dashboard.
- REST API with an OpenAPI spec, optional password and static API-key auth, and
  a Vue 3 web UI (profiles, connections, deployments, live progress, serial
  console viewer).
- Restore validation: every fill/incremental stamps each data disk with an
  identity marker and a per-file SHA-256 manifest; a `verify` run reads the
  data back and proves a backup restore round-tripped intact.
- Discovery & adoption: a restored ghost VM (fresh MAC, unknown to the
  controller) PXE-boots into the temp OS with a report-only discovery token,
  is inspected for GhostFleet identity, and can be adopted — rebind into its
  original deployment (restore-in-place) or into a new `adopted` deployment
  (restore-alongside) for verify/incremental runs without touching the
  original fleet. `GHOSTFLEET_DISCOVERY=off` disables discovery boots.
- Operational hardening: idempotent deploys, run/job cancellation, profile
  import/export, restart resilience, agent retry/resume, and fill boot
  resilience.
- Distribution as prebuilt container images on GHCR with a pull-only compose
  stack; see [docs/INSTALL.md](docs/INSTALL.md).
- Schedules: timer-based run kickoff per deployment — recurring by interval
  (`every`, e.g. an incremental every 24 h), at a wall-clock time (`daily`,
  e.g. power on at 06:00 with an optional weekday filter and timezone) or
  one-shot (`once`, e.g. a teardown in 30 days). Fire times are persisted
  (fires missed while the controller was down are caught up once); a busy
  deployment is retried for up to an hour, then the occurrence is skipped.
  Runs started by a timer are marked in the run history. Existing schedules can
  be edited, paused/resumed and deleted in place.

[Unreleased]: https://github.com/PureStorage-OpenConnect/ghostfleet/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/PureStorage-OpenConnect/ghostfleet/releases/tag/v1.1.0
[1.0.0]: https://github.com/PureStorage-OpenConnect/ghostfleet/releases/tag/v1.0.0
