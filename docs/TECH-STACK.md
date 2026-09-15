# Tech Stack

All choices below are decided and implemented; alternatives and rationale are
kept for the record.

**Guiding principle (NFR-8, decided):** reuse existing open source components
wherever a good one exists — dnsmasq, iPXE, Alpine, fio, SQLite are all
expressions of this. Custom code is reserved for the actual domain logic
(profiles, reconciliation, seeding/planning, plugins, UI) and for cases where
calibration tests prove an off-the-shelf tool can't hit the targets.

## Summary

| Layer | Choice | Why |
|---|---|---|
| Backend + agent language | **Go** | One language for controller core *and* the in-VM agent; the data-generation engine is shared code, compiled into a single static binary that drops into an initramfs. High-throughput generation needs compiled-language speed. |
| vSphere SDK | **govmomi** | The canonical Go vSphere library (used by Terraform, Packer, kubernetes-cloud-provider). Mature tagging + CBT-relevant APIs. |
| Web framework | Go stdlib `net/http` (1.22+ pattern routing — no router dependency), OpenAPI spec | Small, boring, long-lived. Live views poll the JSON API — no WebSocket/SSE needed at this scale. |
| Database | **SQLite** (modernc.org/sqlite, pure Go) | Single-writer workload, one file to back up, zero extra container. |
| Frontend | **Vue 3 + TypeScript + Vite + Tailwind CSS** | Reactive, modern, simple (UI-2) with low ceremony. Built SPA is served by the core container — no Node at runtime. |
| Charts | Inline SVG sparklines (hand-rolled) | The throughput time series needed one small sparkline, not a chart library. |
| Netboot services | **dnsmasq** (DHCPv6 + RA + TFTP) + **iPXE** (pinned commit, embedded chainload script) + HTTP artifacts served by core | dnsmasq does all three isolated-net services in one battle-tested daemon; its config is static, templated from `GHOSTFLEET_ISOLATED_IF` at container start. |
| Write engine (in-VM) | **fio**, orchestrated by the Go agent | Reuses fio's proven compressibility/dedup/seed knobs and throughput instead of reinventing them; the calibration spike confirmed no custom composer is needed — see §"fio as the write engine". |
| Temp OS | **Alpine Linux** virt kernel + custom initramfs (busybox rootfs, fio, xfsprogs, the agent as init hand-off) | ~21 MiB total, boots in seconds (NFR-4) — Buildroot was considered and not pursued. |
| Containerization | Docker Compose (compatible with Podman) | Three services (core, netboot, one-shot tempos) + volumes; upgrade = `compose pull && up -d` (NFR-5). |
| Controller VM OS | **Ubuntu 26.04 LTS** *(decided, DD-12)* | Current LTS, 5 years of standard support. |

## Language: why Go over Python (the main alternative)

Python (FastAPI + pyvmomi) would be faster to prototype the API and has SDKs
for every target platform. But two factors push toward Go:

1. **The agent.** It must run inside a minimal in-memory OS and write data at
   disk speed. A static Go binary (~10 MB, zero dependencies) embeds directly
   into the initramfs. Python in an initramfs means shipping an interpreter
   and dependency tree, and pure-Python generation won't saturate disks —
   you'd end up writing the hot path in something compiled anyway.
2. **Shared generation engine.** Compressibility/dedup math must be *identical*
   in the controller (planning, predictions, verification) and the agent
   (execution). One Go module used by both eliminates a whole class of drift bugs.

Non-vSphere platforms are plain REST APIs (Nutanix, Proxmox) or
WinRM/PowerShell (Hyper-V) — no Python-only SDK advantage there.

**Alternative considered:** Python controller + Go agent. Workable, but splits
the generation engine across two languages (drift risk) and two toolchains.

**Decided:** Go. Note the confirmed constraint: the controller does **no**
payload generation at all — it plans and orchestrates; only the in-VM agents
write data. "Shared engine" therefore means shared *planning/seed* logic and,
if needed, the chunk composer used by the agent.

## fio as the write engine (decided — calibration spike confirmed, no custom composer)

Rather than writing all generation from scratch, the agent should drive
**fio** wherever its knobs map onto the profile:

| Profile knob | fio feature | Fit |
|---|---|---|
| Compressibility | `buffer_compress_percentage` / `buffer_pattern` | ✅ direct |
| Dedup within VM | `dedupe_percentage` (+ `dedupe_mode`) | ✅ direct (per job) |
| Deterministic content | `randseed` | ✅ direct |
| Throughput cap (DD-11) | `rate=` | ✅ direct |
| Per-run write statistics | fio JSON output | ✅ direct |
| Realistic file tree | — | ❌ agent creates the tree, fio fills the files |
| **Dedup across VMs** | — | ❌ agent writes a controlled fraction of files from deployment-global seeds (identical on all VMs) |
| File-level change/growth | — | ❌ agent selects files per run-seed and re-invokes fio on them |

So the Go agent owns: filesystem setup, file-tree planning, cross-VM dedup
file sets, run orchestration — and shells out to fio for the heavy writing.

**Calibration spike result (2026-06-15, fio 3.41 on the controller VM):**
fio is confirmed as the engine — the custom composer is not needed. Measured
against real zstd-3 and fixed-block dedup analysis:
`buffer_compress_percentage` 50/90 → 49.6 % / 89.7 % zstd savings;
`dedupe_percentage` 25/50 → 21.9 % / 51.6 % duplicate blocks; the two compose
(compress 50 + dedupe 50 → 73.8 % zstd savings *and* 51.6 % dup blocks
independently). `randseed` makes output byte-identical across runs (so runs
are reproducible **and** cross-VM dedup works by writing a "global" data
portion from a deployment-wide seed identical on every VM); a different seed
yields different bytes (per-VM data is genuinely unique). fio is packaged into
the temp-OS image and doubles as a free disk-benchmark tool inside the VMs.

## Frontend: why Vue 3 (decided, DD-9)

Requirement is "reactive, modern, simple" with live-updating dashboards —
any of Vue 3 / React / Svelte delivers. With no in-house preference, the
proposal stands: **Vue 3 + TypeScript + Vite + Tailwind** — current
mainstream best practice with low boilerplate and a gentle learning curve
for future maintainers.

## Netboot: why dnsmasq + iPXE

- dnsmasq provides DHCPv6, router advertisements and TFTP in one daemon with
  simple config — ideal for a single-interface isolated network.
- iPXE as second-stage loader lets us switch from TFTP to fast HTTP downloads
  and gives scriptable boot (per-VM parameters via URL).
- Constraint to be aware of: **IPv6 netboot requires UEFI VMs** (BIOS PXE is
  IPv4-only). All target platforms support UEFI guests; vSphere supports
  IPv6 PXE/HTTP boot with EFI firmware. Fallback if a platform misbehaves:
  the isolated net can run a private IPv4 range purely link-locally — it is
  isolated, so collisions can't propagate. Discussed in DD-3.

## Persistence: why SQLite

One controller, one writer, modest data volume (profiles, runs, metric
summaries). SQLite removes a container, a connection pool, and a failure mode.
If multi-controller or heavy time-series needs appear later, the storage layer
sits behind a repository interface so Postgres can be swapped in.

## License (decided: Apache-2.0)

The project is published open source under **Apache-2.0** (LICENSE in the
repo root). The reasoning behind the choice:

- **Apache-2.0 (recommended):** everything MIT offers, plus an **explicit
  patent grant** from every contributor and explicit contribution terms.
  For infrastructure tooling aimed at enterprise environments (backup
  vendors, labs), this is the license that makes corporate legal teams and
  potential contributors comfortable. Used by Kubernetes, Terraform (pre-BSL),
  and most of the Go ecosystem this project builds on.
- **MIT:** shorter and maximally simple, but no patent language. Fine too —
  just weaker protection for users and contributors.

Compatibility check of the components used: govmomi, Vue, Tailwind,
modernc.org/sqlite are Apache/MIT/BSD-licensed — no conflicts. **fio is
GPL-2.0**, but the agent only *executes* it as a separate process (no
linking), which is unproblematic; the temp-OS image that ships the fio binary
simply honors GPL source-availability, like every Linux distro does. dnsmasq
(GPL) runs as its own container, same situation.

## Project layout (actual)

```
/cmd/controller        # core service main
/cmd/agent             # temp-OS agent main (also the initramfs PID 1 hand-off)
/internal/api          # REST + boot/agent endpoint handlers, OpenAPI spec
/internal/orch         # orchestration engine (reconcile, fills, watchdogs)
/internal/datagen      # planning/seeding (hierarchical FNV seeds, work orders)
/internal/hypervisor   # plugin interface + /vsphere driver + /fake (tests)
/internal/model        # domain types (profiles, deployments, placement)
/internal/store        # SQLite repositories, migrations
/internal/secrets      # encrypted credential store (AES-256-GCM)
/web                   # Vue 3 SPA
/deploy                # compose files (dev + dist), Dockerfiles (controller,
                       # netboot incl. iPXE build, tempos), preflight.sh
/examples              # automation examples (backup-job post script)
/tools/vcsim           # local vSphere API simulator for development
/docs
```
