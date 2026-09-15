# Requirements

Consolidated from the initial project description. Each requirement has an ID so we
can reference them in design discussions, issues and commits.

Keywords: **MUST** (required), **SHOULD** (strongly desired), **MAY** (optional/nice-to-have).

## 1. Deployment model

| ID | Requirement |
|----|-------------|
| DEP-1 | The controller runs as a VM **inside the target environment** the test data is created on. The controller VM is deployed **manually** by the operator. |
| DEP-2 | The controller VM OS reference is **Ubuntu 26.04 LTS** *(decided, DD-12)*; in practice any Linux that runs Docker + compose works — the whole stack is containerized (see INSTALL.md). |
| DEP-3 | The controller software **MUST** run containerized (single compose stack) where possible and feasible. |
| DEP-4 | The controller VM has **two network interfaces**: a *management network* (routable/"public" in the lab sense) and an *isolated network*. |
| DEP-5 | Admin access to the controller VM is via **SSH** over the management network. |
| DEP-6 | The controller **web interface** is reachable over the management network. |

## 2. Isolated network services

| ID | Requirement |
|----|-------------|
| NET-1 | On the isolated network, the controller provides **DHCP** and **PXE/network-boot** services. |
| NET-2 | The isolated network **SHOULD** use **IPv6 only**, so no IPv4 address collisions with the surrounding environment are possible. |
| NET-3 | Generated VMs boot a **temporary OS** (in-memory) over the network from images/kernels served by the controller. |
| NET-4 | Generated-VM agents communicate status/progress to the controller over the isolated network. |

*Note:* PXE over IPv6 requires **UEFI firmware** on the generated VMs — classic BIOS
PXE is IPv4-only. **Decided (DD-3): generated VMs are always UEFI.**

## 3. Hypervisor integration

| ID | Requirement |
|----|-------------|
| HYP-1 | The controller manages hypervisors through a **plugin-capable integration layer**. |
| HYP-2 | **vSphere/vCenter** is the first-class, initial integration. Target versions: **vSphere 8 (priority) and vSphere 9**; no older versions *(decided, DD-7)*. |
| HYP-3 | Further candidate plugins (deferred until vSphere works well — DD-8): standalone **ESXi** (native, no vCenter), **Nutanix AHV**, **Proxmox VE**, **Hyper-V**. Only the plugin *interface* must accommodate them now. |
| HYP-4 | A hypervisor "management connection" (endpoint, credentials, placement options like cluster/datastore/network mapping) is configured in the web UI and validated before use. |

## 4. Source data profiles

| ID | Requirement |
|----|-------------|
| PRO-1 | A profile defines: **number of VMs**, a **naming scheme** for them (prefix + zero-padded counter, e.g. `bkupsrc-0001`; prefix overridable per deployment for uniqueness — DD-13), **disks per VM**, **disk size**, and **amount of data per disk**. |
| PRO-2 | A profile defines data characteristics: **compressibility**, **deduplicability within a VM**, **deduplicability across VMs**, a **change rate**, and a **growth rate**. Change/growth rates apply **per incremental run** *(decided, DD-5)*; changes are distributed **uniformly** over existing data (hot-spot model later, DD-6). |
| PRO-3 | A profile defines a **tag** that is attached to all VMs created from it (e.g. vSphere tag/category, or attribute/label on other platforms). |
| PRO-4 | Profiles have **names** and are **versioned** (every change creates a new version; deployments record which version they originated from). |
| PRO-5 | Profile behavior after generation completes is configurable: VMs **shut down** or **stay running**. |
| PRO-6 | A **deployment** can be **extended** at any time (more VMs, more disks, more data) by adjusting its effective configuration; the deployment is reconciled to the new desired state. **Shrinking** below the deployed state is not supported (out of scope), but full teardown is (PRO-7). |
| PRO-7 | The complete deployment (all VMs and disks) can be **deleted** while the profile (and its versions) and the deployment's run history are **kept**. |
| PRO-8 | A profile is a **reusable template**: deploying it creates an **independent deployment** whose effective configuration is a snapshot of the chosen profile version plus deploy-time overrides. Later profile edits never affect existing deployments. |
| PRO-9 | At deploy time the operator **selects compute and storage placement** (e.g. vSphere cluster/host/resource pool, datastore) and **may adjust any profile settings** for this deployment. |
| PRO-10 | **Multiple deployments can exist and run concurrently**, including several from the same profile. VM/disk names must remain unique across deployments (naming scheme includes a deployment token — see DD-13). |
| PRO-11 | Generated-VM **compute sizing (vCPU/RAM)** is a profile setting (default 2 vCPU / 2 GiB) — it can affect data generation speed. Disk **provisioning type** is a profile setting: **thin (default)** or thick *(decided, DD-10)*. |
| PRO-12 | An optional **aggregate write-rate cap per deployment** can be set; the controller redistributes the budget across actively writing VMs (a staggered deployment lets late VMs use freed-up headroom) *(decided, DD-11)*. *Status: implemented as a static per-wave split; dynamic mid-run redistribution is listed as an idea in ROADMAP.md.* |

## 5. Deployment & data generation lifecycle

| ID | Requirement |
|----|-------------|
| GEN-1 | "Deploy profile" creates the VMs via the hypervisor plugin, attaches them to the isolated network, creates the configured number/size of disks per VM, and names everything per the naming scheme. |
| GEN-2 | Booted VMs run the temporary OS which executes the **initial data fill** according to the profile and reports progress. |
| GEN-3 | An **incremental run** can be triggered at any time after the initial fill: agents apply the profile's change rate and growth rate across the disks. VMs are re-booted into the temp OS if they were shut down, or reused if still running. |
| GEN-4 | Data generation MUST be able to hit the configured compressibility / intra-VM dedup / cross-VM dedup targets in a **repeatable** (deterministic, seed-based) way. |
| GEN-5 | The operator can run any sequence of: deploy → fill → (backup, external) → incremental → (backup) → scale-up → ... |
| GEN-6 | Generated disks carry a **real filesystem with files** (**XFS** directly on the whole disk, no partition table — keeps the temp OS minimal; other FS types possible later — DD-16); data characteristics apply to the file contents. No file-tree realism required (backup tests are VM-level/block-based) — the layout is a simple organizing structure with large fixed-size files *(decided, DD-17)*. |
| GEN-7 | The controller performs **no payload data generation** — it plans, seeds and orchestrates only. All data is written exclusively by the agents inside the test-source VMs. |

## 6. Web interface

| ID | Requirement |
|----|-------------|
| UI-1 | Protection by an **optional password** — the UI MUST also be usable without a password if none is set. |
| UI-2 | **Reactive, modern, simple** design; live-updating views during generation runs. |
| UI-3 | Configuration of hypervisor connections and source data profiles. |
| UI-4 | Trigger and monitor: deploy, initial fill, incremental runs, scale-up, teardown. |
| UI-5 | **Statistics** per deployment and per run, including at least: number of VMs/disks, total data deployed/written, duration of initial fill, duration of each incremental run, write throughput (MB/s) per VM and aggregated over the deployment. Delivery: **on-screen + JSON API**; no CSV export/webhooks *(decided, DD-14)*. |

## 7. Non-functional

| ID | Requirement |
|----|-------------|
| NFR-1 | Hypervisor support is **extensible** (plugin interface; adding a platform must not require touching core logic). |
| NFR-2 | All state (profiles, versions, deployments, run history, metrics) is **persisted** on the controller and survives controller restarts. |
| NFR-3 | Generation is **resumable/robust**: a failed or interrupted run can be retried without corrupting the desired-state model. |
| NFR-4 | The temp OS image is small and boots fast (target: < 1 min from power-on to agent-ready on typical lab hardware). |
| NFR-5 | Controller-side install/upgrade is simple: prebuilt images + `docker compose pull && docker compose up -d` — no source checkout or toolchain on the controller VM (see INSTALL.md). |
| NFR-6 | Secrets (hypervisor credentials) are stored encrypted at rest, never returned in plain text by the API. |
| NFR-7 | Design scale envelope per deployment: up to **200 VMs** and **50 TB** of generated data *(decided, DD-1)*. |
| NFR-8 | **Prefer existing open source solutions over custom implementations** wherever a good option exists (e.g. dnsmasq, iPXE, Alpine, fio). Custom code needs a justified reason (missing capability, calibration precision, integration glue). |

## Explicitly out of scope

- Performing or validating **backups** themselves — the simulator only produces source data.
- Production-grade multi-user auth / RBAC (single optional password only, see UI-1).
- Shrinking a deployment (only grow or full teardown).
