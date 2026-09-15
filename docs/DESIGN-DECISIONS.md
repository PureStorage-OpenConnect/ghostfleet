# Design Decisions

A record of the design questions that shaped GhostFleet and how each was
resolved, kept for rationale and future reference.

## Decisions

| ID | Decision |
|----|----------|
| DD-1 | **Scale target: ≤200 VMs, ≤50 TB per deployment.** Bounded concurrency + optional throttling; SQLite stays sufficient. |
| DD-2 | **Filesystem from day one.** Agents partition disks, create a filesystem and write real files; content follows the generation model. Raw-block mode may come later as an option, not the default. Follow-ups: DD-16, DD-17. |
| DD-3 | **UEFI-only generated VMs accepted** → isolated network stays IPv6-only. |
| — | **Language: Go** for controller and agent. Constraint confirmed: the **controller never generates payload data** — it only plans, seeds and orchestrates; all data is written exclusively by the agents inside the test-source VMs. **fio** was evaluated and adopted as the agent's write engine (see TECH-STACK.md §"fio"). |
| DD-4 | **Temp OS: Alpine netboot.** Confirmed together with a general **design principle: prefer existing open source solutions over building our own** wherever a good option exists (now NFR-8 in REQUIREMENTS.md). |
| DD-5 | **Change/growth rates apply per incremental run** ("each run changes X % and grows by Y %"). |
| DD-6 | **Uniform** change distribution first; hot-spot model is a later profile option. |
| DD-7 | **vSphere 8 is the priority target; vSphere 9 desirable.** No older versions. |
| DD-8 | **vSphere only for now.** The plugin interface stays platform-neutral so other hypervisors can be added easily, but no second implementation until vSphere works well; priority list revisited then. |
| DD-9 | No frontend preference / knowledge — going with the proposal: **Vue 3 + TypeScript + Vite + Tailwind**. |
| DD-10 | **VM sizing (vCPU/RAM) is a profile setting** (default 2 vCPU / 2 GiB; affects generation speed) and **thin/thick provisioning flag, default thin**. |
| DD-11 | **Optional write-rate cap per deployment** (aggregate budget): the controller redistributes the budget across the VMs actively writing, so in a staggered deployment late VMs can use the headroom (e.g. 12 VMs, 10 writing in parallel → the last 2 may run at 5× per-VM speed). |
| — | **Profiles are reusable templates; deployments are independent instances.** At deploy time the operator selects compute/storage placement in the cluster and may adjust profile settings; the deployment then keeps its own effective configuration, unaffected by later profile edits. Multiple deployments (even from the same profile) can exist and run concurrently. (REQUIREMENTS.md PRO-8…PRO-10, ARCHITECTURE.md §2.) |
| DD-12 | **Controller OS: Ubuntu 26.04 LTS** (current, 5 years of support). |
| DD-13 | **Naming: prefix + zero-padded counter** (e.g. `bkupsrc-0001`). Uniqueness across concurrent deployments: the prefix is part of the deployment's effective config (defaulted from the profile, overridable at deploy time); the controller warns on prefix collisions with existing deployments on the same connection. |
| DD-14 | **Statistics: on-screen + JSON API** is sufficient. No CSV export / webhooks planned. |
| DD-15 | **Open source, Apache-2.0 (confirmed).** See TECH-STACK.md §"License"; LICENSE file in repo root. |
| DD-16 | **XFS only** for now (default); no ext4 needed. Filesystem choice stays an internal parameter of the agent so other types (ext4, NTFS) can be added later. |
| DD-17 | **No file-tree realism needed** — backup tests are VM-level/block-based. The agent uses a simple *organizing* layout (e.g. directories separating cross-VM-dedup data, VM-local data, and per-run growth data) with large fixed-size files for write efficiency. |

New design questions get added here with fresh IDs as they come up.
