# Changelog

All notable changes to GhostFleet are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
project follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- A fill/incremental/verify run started within 30 s of the previous run's
  post-run shutdown booted nothing: the boot pass trusted the still-fresh agent
  heartbeats and skipped the powered-off VMs, leaving the run "running" until
  the boot watchdog power-cycled the fleet five minutes later. The boot pass now
  goes by the hypervisor's actual power state, and the post-run shutdown clears
  agent liveness so a powered-off VM never shows an online agent.

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

[1.0.0]: https://github.com/PureStorage-OpenConnect/ghostfleet/releases/tag/v1.0.0
