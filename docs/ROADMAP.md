# Roadmap

GhostFleet 1.0.0 is feature-complete for its initial scope. This document lists
what is implemented and what may come next. The how-and-why of the technical
choices lives in [ARCHITECTURE.md](ARCHITECTURE.md),
[TECH-STACK.md](TECH-STACK.md) and [DESIGN-DECISIONS.md](DESIGN-DECISIONS.md).

## Implemented

- **Core domain & API** — SQLite-backed profiles (versioned), hypervisor
  connections (encrypted credentials), and deployments with effective-config
  snapshots and delete-keeps-history semantics; a REST API with an OpenAPI spec;
  optional password / API-key auth; and a Vue 3 web UI.
- **vSphere plugin** — connection validation, placement enumeration
  (cluster / host / datastore / folder / resource pool), create/delete UEFI VMs,
  tag handling, and power operations. Deploy / scale-up / teardown are built on
  desired-vs-actual reconciliation and are retryable after partial failures.
  Includes an owner-aware cross-deployment conflict flow (adopt / clean / abort).
- **IPv6 PXE boot chain** — dnsmasq (RA + stateful DHCPv6 + TFTP) + iPXE + an
  in-memory Alpine temp OS that hands off to the agent.
- **Calibrated data generation** — fio-driven XFS fills with configurable size,
  compressibility and within-/cross-VM deduplicability, using deterministic
  hierarchical seeding, with live progress and throughput reporting.
- **Incremental runs & statistics** — in-place, CBT-visible change and growth
  with no re-mkfs; run history, accurate write-phase throughput, and a stats
  dashboard with inline sparklines.
- **Restore validation (verify runs)** — every fill/incremental stamps each
  disk with an identity marker and a per-file SHA-256 manifest; a verify run
  reads everything back and proves a backup restore round-tripped intact.
- **Discovery & adoption** — restored ghost VMs (fresh MAC) PXE-boot into a
  report-only discovery mode, are inspected for GhostFleet identity, and can
  be adopted: rebind into their original deployment (restore-in-place) or
  into a new adopted deployment (restore-alongside) for verify/incrementals.
- **Schedules** — timer-based run kickoff per deployment: recurring by
  interval or at a wall-clock time (with weekday filter and timezone), or
  one-shot (e.g. a teardown in 30 days). Persisted fire times survive
  controller restarts; missed fires are caught up once.
- **Operational hardening** — idempotent deploys, run/job cancellation, static
  API keys, profile import/export, restart resilience, agent retry/resume, and
  fill boot resilience (wedged VMs are power-cycled; unbootable VMs fail alone).
- **Distribution** — prebuilt container images on GHCR, built by a release
  workflow on every version tag, plus a pull-only compose stack; see
  [INSTALL.md](INSTALL.md).

## Ideas / not yet scheduled

- Further hypervisor plugins (Proxmox VE, ESXi-native, Nutanix AHV, Hyper-V) —
  the plugin interface is platform-neutral by design.
- Additional filesystems on generated disks (ext4, NTFS) and a raw-block mode
  (no filesystem) as a profile option.
- Absolute-or-percent quantity inputs for change rate, growth rate and data
  amount (currently percentages).
- Dynamic mid-run redistribution of the aggregate write-rate cap.
- Hot-spot change distributions.
- Export of run statistics (CSV / JSON) and a webhook on run completion.
- Support for containers and physical machines as data-generation targets.
- Packaging niceties: an OVA appliance, a cloud-init user-data example, and a
  multi-arch controller image.
