# vSphere Setup: Least-Privilege Service Account

How to give GhostFleet exactly the access it needs and nothing more: a
**custom role**, a **dedicated user**, and role assignments on **specific
inventory objects** — one resource pool, one VM folder, one datastore, one
or two portgroups. A mistake in GhostFleet can then only ever affect objects
inside that sandbox.

## How vSphere authorization works (30 seconds)

A *permission* in vSphere is the triple **user + role + inventory object**
(with a *propagate to children* flag). Roles are just named bundles of
privileges; they do nothing until assigned on an object. Least privilege
therefore means: one role containing only the privileges GhostFleet calls,
assigned only on the objects GhostFleet should touch. That is compute
(resource pool), storage (datastore), network (portgroup),
plus one piece that's easy to miss: the **VM folder** (VM create/delete is
checked against the *folder*, not the pool) and **tagging** (checked against
*global permissions*, not inventory objects).

## 1. The custom role

Create a role named `GhostFleet` (vSphere Client: **Administration → Access
Control → Roles → +**) with exactly these privileges:

| UI category | Privilege (UI label) | API ID | Used for |
|---|---|---|---|
| Datastore | Allocate space | `Datastore.AllocateSpace` | creating/growing VMDKs |
| Network | Assign network | `Network.Assign` | connecting the NIC at VM creation |
| Resource | Assign virtual machine to resource pool | `Resource.AssignVMToPool` | placing VMs into the pool |
| Virtual machine → Edit Inventory | Create new | `VirtualMachine.Inventory.Create` | deploy |
| Virtual machine → Edit Inventory | Remove | `VirtualMachine.Inventory.Delete` | teardown |
| Virtual machine → Change Configuration | Add new disk | `VirtualMachine.Config.AddNewDisk` | disks at create + scale-up |
| Virtual machine → Change Configuration | Add or remove device | `VirtualMachine.Config.AddRemoveDevice` | NIC at create |
| Virtual machine → Change Configuration | Extend virtual disk | `VirtualMachine.Config.DiskExtend` | disk-size scale-up |
| Virtual machine → Change Configuration | Change Settings | `VirtualMachine.Config.Settings` | annotations/rename |
| Virtual machine → Change Configuration | Change Memory | `VirtualMachine.Config.Memory` | future VM resizing |
| Virtual machine → Change Configuration | Change CPU count | `VirtualMachine.Config.CpuCount` | future VM resizing |
| Virtual machine → Interaction | Power on | `VirtualMachine.Interact.PowerOn` | boot for PXE at fill start |
| Virtual machine → Interaction | Power off | `VirtualMachine.Interact.PowerOff` | post-run shutdown, wedged-VM power-cycling, teardown |
| vSphere Tagging | Assign or Unassign vSphere Tag | `InventoryService.Tagging.AttachTag` | PRO-3 tags |
| vSphere Tagging | Create vSphere Tag | `InventoryService.Tagging.CreateTag` | auto-create on first use¹ |
| vSphere Tagging | Create vSphere Tag Category | `InventoryService.Tagging.CreateCategory` | auto-create on first use¹ |

¹ Omit the two *Create* privileges if you pre-create the category/tag
manually — then GhostFleet only ever attaches existing tags.

## 2. The sandbox objects

Create once (names are examples):

- **Resource pool** `ghostfleet` under your cluster — optionally with CPU/RAM
  limits, which also caps how much load generated fleets can put on the
  cluster.
- **VM folder** `ghostfleet` in the datacenter's VMs-and-Templates view.
  GhostFleet uses a folder named `ghostfleet` by default; pre-creating it
  means the role needs no folder-create privilege. **A nested subfolder
  works just as well** (permissions are checked on the folder itself and
  propagate downward — depth is irrelevant). Reference it in the
  deployment's "VM folder" field as `vm/team-a/ghostfleet`, as an absolute
  inventory path (`/YourDC/vm/team-a/ghostfleet`), or by bare name if it is
  unique in the datacenter.
- Decide which **datastore** and which **portgroup(s)** GhostFleet may use
  (later: the isolated IPv6 portgroup; plus the VM network if you ever want
  NICs there).

## 3. The user

**Administration → Single Sign On → Users and Groups → vsphere.local →
Add User**: e.g. `ghostfleet@vsphere.local` with a generated password.
(An AD/LDAP account works identically — permissions just reference the
principal.)

## 4. The permission assignments

Right-click each object → **Add Permission** → user `ghostfleet@vsphere.local`:

| Object | Role | Propagate | Why |
|---|---|---|---|
| Datacenter | Read-only | **no** | resolve inventory paths, see the DC |
| Cluster | Read-only | **no** | enumerate the cluster in the placement picker |
| Host (only if using host-pinned placement) | Read-only | no | enumerate/resolve the host |
| Resource pool `ghostfleet` | GhostFleet | **yes** | `Resource.AssignVMToPool` |
| VM folder `ghostfleet` | GhostFleet | **yes** | VM create/delete/configure/power — propagation covers every VM inside |
| Datastore | GhostFleet | no | `Datastore.AllocateSpace` |
| Portgroup(s) | GhostFleet | no | `Network.Assign` |
| **Global Permissions** (Administration → Access Control → Global Permissions) | GhostFleet | yes | tagging privileges are evaluated **globally**, not on inventory objects² |

² This is the one assignment that can't be scoped to an object — vSphere
evaluates tagging via Global Permissions. The role's other privileges being
present globally does **not** grant VM/datastore rights anywhere, because
those are still checked against inventory objects where the user has no
permission. If even global tag-create feels too broad: pre-create the
category/tag, drop the two Create privileges, and keep only AttachTag.

## 5. Wire it up in GhostFleet

1. Connections → New connection: endpoint `https://<vcenter>/sdk`, username
   `ghostfleet@vsphere.local`.
2. **Test** — should report the vCenter product/version. A 502 here means
   credentials or TLS, not privileges.
3. New deployment → the placement picker will only offer what the account
   can see: your cluster, the `ghostfleet` **resource pool** (select it —
   "Cluster default" would try the cluster root pool and fail for this
   user), the permitted datastore and portgroup. That reduced list *is* the
   guardrail working.

## 6. govc alternative (scriptable)

With [govc](https://github.com/vmware/govmomi/tree/main/govc) and admin
credentials (`GOVC_URL`, `GOVC_USERNAME`, `GOVC_PASSWORD` env vars):

```sh
DC=YourDC CLUSTER=YourCluster DS=YourDatastore PG=YourPortgroup

govc sso.user.create -p 'S0meStrongPass!' ghostfleet

govc role.create GhostFleet \
  Datastore.AllocateSpace Network.Assign Resource.AssignVMToPool \
  VirtualMachine.Inventory.Create VirtualMachine.Inventory.Delete \
  VirtualMachine.Config.AddNewDisk VirtualMachine.Config.AddRemoveDevice \
  VirtualMachine.Config.DiskExtend VirtualMachine.Config.Settings \
  VirtualMachine.Config.Memory VirtualMachine.Config.CpuCount \
  VirtualMachine.Interact.PowerOn VirtualMachine.Interact.PowerOff \
  InventoryService.Tagging.AttachTag InventoryService.Tagging.CreateTag \
  InventoryService.Tagging.CreateCategory

govc folder.create "/$DC/vm/ghostfleet"
govc pool.create "/$DC/host/$CLUSTER/Resources/ghostfleet"

P=ghostfleet@vsphere.local
govc permissions.set -principal $P -role ReadOnly  -propagate=false "/$DC"
govc permissions.set -principal $P -role ReadOnly  -propagate=false "/$DC/host/$CLUSTER"
govc permissions.set -principal $P -role GhostFleet -propagate=true  "/$DC/host/$CLUSTER/Resources/ghostfleet"
govc permissions.set -principal $P -role GhostFleet -propagate=true  "/$DC/vm/ghostfleet"
govc permissions.set -principal $P -role GhostFleet -propagate=false "/$DC/datastore/$DS"
govc permissions.set -principal $P -role GhostFleet -propagate=false "/$DC/network/$PG"
```

The **Global Permission** for tagging is the one step govc cannot do — set
it in the vSphere Client UI (Administration → Access Control → Global
Permissions → +, user `ghostfleet@vsphere.local`, role `GhostFleet`,
propagate ✓).

## Shared-lab reality: no rights to create roles or global permissions

vSphere has **three separate authorization realms**, and being "Administrator"
in one grants nothing in the others:

1. **SSO domain admin** — creating users/groups in `vsphere.local`, identity
   sources. Governed by membership in the `vsphere.local\Administrators`
   *group*; no role grants this.
2. **Global Permissions** — a global root *above* every vCenter inventory;
   tags, tag categories and content libraries live here. Only holders of a
   *global* Administrator permission can manage global permissions (and, in
   practice, roles in shared setups). An inventory-level Administrator
   cannot.
3. **Inventory permissions** — the vCenter object tree. This is where a
   "Domain Users → Administrator at vCenter level" grant applies.

If you have inventory Administrator (e.g. via an AD group) but no access to
realms 1–2, the pragmatic path:

- Create a **dedicated AD service user** (you likely can, as AD admin). Via
  the group grant it inherits inventory Administrator — more than the
  GhostFleet role, but dedicated credentials still beat personal ones in the
  encrypted store, and they can be scoped down later.
- **Leave the profile Tag empty** — tagging is GhostFleet's only feature
  that touches the global-permission realm. Empty tag = the tagging API is
  never called = no global permission needed.
- The sandbox is then *organizational* (the resource pool/folder/datastore
  you select at deploy time) rather than enforced. To get real enforcement
  later, the lab admin needs to do exactly two things: create the custom
  role from §1, and add one global permission for the tagging privileges —
  and AD group hygiene must exclude the service account from the broad
  Administrator grant (vSphere has no "deny" permissions; a broader grant
  always wins).

## Single-host setup without VLANs (standard vSwitches)

If the lab has no distributed switches and no spare VLAN — host-local
standard vSwitches only — GhostFleet still works, and the isolated network
gets even simpler: **it doesn't need a VLAN or an uplink at all.**

On the chosen ESXi host:

1. Create a **new standard vSwitch with no physical uplinks** (or reuse one)
   and add a portgroup, e.g. `ghostfleet-isolated`. No uplink = traffic can
   never leave the host = no VLAN, no trunk involvement, no collisions —
   exactly the isolation NET-2 wants.
2. Attach the **controller VM's second NIC** (the one configured as
   `GHOSTFLEET_ISOLATED_IF`) to that portgroup.
3. In the GhostFleet deployment form, select that **host under "Host (pin
   VMs)"** and `ghostfleet-isolated` as the network. Every generated VM is
   created on that host and wired to the portgroup.

Pinning stays stable on its own: a VM connected to a portgroup that exists
only on one host is not vMotion-compatible with any other host, so DRS
leaves it alone. (Belt-and-suspenders: set DRS to manual for these VMs via a
VM override — but it shouldn't be necessary.) The controller VM itself must of course live on the same host;
pin it the same way or via a DRS must-rule.

This is the recommended topology for a first single-host test. Scaling beyond one host later
just means a VLAN-backed portgroup with the same name on every host (or a
DVS portgroup) — no GhostFleet changes needed.

## Troubleshooting

| Symptom | Likely missing |
|---|---|
| Connection "Test" fails (502) | wrong endpoint/credentials, or TLS — try "skip TLS verification" for self-signed vCenter certs |
| Placement picker is empty | Read-only on datacenter/cluster |
| Deploy fails `Permission to perform this operation was denied` on create | `VirtualMachine.Inventory.Create` on the **folder**, or `Resource.AssignVMToPool` on the pool — also check the deployment actually selected the `ghostfleet` resource pool, not "Cluster default" |
| Create fails referencing the datastore | `Datastore.AllocateSpace` on the datastore |
| Create fails at the NIC | `Network.Assign` on the portgroup |
| VMs created but tagging fails | the Global Permission (see footnote ²) |
| Scale-up disk grow fails | `VirtualMachine.Config.DiskExtend` |

> ⚠ Written against vSphere 8 documentation and tested against vcsim, which
> does not enforce permissions. GhostFleet itself is validated end-to-end on
> real vSphere 8.0.3 (deploy, boot chain, fills, tags — using a broader lab
> account); the **least-privilege role above has not yet been validated on a
> real vCenter** — reports from real deployments are welcome as GitHub
> issues. Two features added after this guide may need more than the §1
> role: the **in-UI serial console viewer** downloads the VM's `console.log`
> via the datastore file API (expect to add `Datastore.Browse`, and report
> back if more is needed), and VM creation attaches that **serial port**
> device (covered by `VirtualMachine.Config.AddRemoveDevice`).
