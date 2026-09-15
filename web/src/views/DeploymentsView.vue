<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import {
  api,
  type Connection,
  type Deployment,
  type DiscoveredVM,
  type DiscoveryDisk,
  type ManagedVM,
  type PlacementOptions,
  type Profile,
  type ProfileSpec,
  type Run,
  type Schedule,
  type ScheduleRequest,
  type VMConflict,
} from '../api'
import SpecForm from '../components/SpecForm.vue'
import SearchableSelect from '../components/SearchableSelect.vue'
import MultiSelect from '../components/MultiSelect.vue'
import Sparkline from '../components/Sparkline.vue'

const deployments = ref<Deployment[]>([])
const profiles = ref<Profile[]>([])
const connections = ref<Connection[]>([])
const error = ref('')
const creating = ref(false)
const expandedVMs = ref<Record<string, ManagedVM[]>>({})
const expandedRuns = ref<Record<string, Run[]>>({})
const expandedSchedules = ref<Record<string, Schedule[]>>({})

// --- scale-up (edit effective config) state ---
const editing = ref<Deployment | null>(null)
const editSpec = ref<ProfileSpec | null>(null)

// --- create form state ---
const form = reactive({
  name: '',
  profileId: '',
  connectionId: '',
  placement: {
    datacenter: '',
    cluster: '',
    host: '',
    resourcePool: '',
    network: '',
    folder: '',
  } as Record<string, string>,
})
const placementOptions = ref<PlacementOptions | null>(null)
const placementError = ref('')
// Datastores to place VMs on; multiple = round-robin spread. Joined into the
// placement's "datastore" key (newline-separated) at submit time.
const datastores = ref<string[]>([])

// Datastore picker options (names) and a free-space/fill-grade hint per name.
const datastoreNames = computed(() => placementOptions.value?.datastores.map((d) => d.name) ?? [])
const datastoreHints = computed(() => {
  const out: Record<string, string> = {}
  for (const d of placementOptions.value?.datastores ?? []) {
    if (d.capacityBytes > 0) {
      const usedPct = Math.round(((d.capacityBytes - d.freeBytes) / d.capacityBytes) * 100)
      out[d.name] = `${formatBytes(d.freeBytes)} free of ${formatBytes(d.capacityBytes)} · ${usedPct}% full`
    }
  }
  return out
})

// Hosts offered in the pin-VMs picker: only those in the selected cluster
// (each host option carries its cluster; standalone hosts carry their own
// compute-resource name). No cluster selected = all hosts.
const clusterHosts = computed(() => {
  const hosts = placementOptions.value?.hosts ?? []
  const c = form.placement.cluster
  return hosts.filter((h) => !c || h.cluster === c).map((h) => h.name)
})

// A cluster change invalidates a host pinned in another cluster.
watch(
  () => form.placement.cluster,
  () => {
    if (form.placement.host && !clusterHosts.value.includes(form.placement.host)) {
      form.placement.host = ''
    }
  },
)

// formatBytes renders a byte count as a compact binary-unit string.
function formatBytes(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}
const spec = ref<ProfileSpec | null>(null)
const adjustSpec = ref(false)

watch(
  () => form.profileId,
  () => {
    const p = profiles.value.find((p) => p.id === form.profileId)
    spec.value = p ? { ...p.spec } : null
  },
)

watch(
  () => form.connectionId,
  async (id) => {
    placementOptions.value = null
    placementError.value = ''
    if (!id) return
    try {
      placementOptions.value = await api.listPlacement(id)
      const o = placementOptions.value
      form.placement.datacenter = o.datacenters?.[0] ?? ''
      form.placement.cluster = o.clusters[0] ?? ''
      form.placement.host = '' // empty = cluster/DRS decides
      form.placement.resourcePool = '' // empty = cluster's root pool
      datastores.value = [] // no default — force an explicit choice (avoid the wrong datastore)
      form.placement.network = o.networks[0] ?? ''
    } catch (e) {
      placementError.value = `Could not load placement options: ${e}`
    }
  },
)

async function reload() {
  ;[deployments.value, profiles.value, connections.value, discovered.value] = await Promise.all([
    api.listDeployments(),
    api.listProfiles(),
    api.listConnections(),
    api.listDiscoveredVMs().catch(() => discovered.value),
  ])
  // Keep any open VM/run panels fresh (e.g. an error appearing after a job).
  for (const id of Object.keys(expandedRuns.value)) {
    expandedRuns.value[id] = await api.listRuns(id).catch(() => expandedRuns.value[id])
    await loadSamples(id, expandedRuns.value[id])
  }
  for (const id of Object.keys(expandedVMs.value)) {
    expandedVMs.value[id] = await api.listDeploymentVMs(id).catch(() => expandedVMs.value[id])
  }
  for (const id of Object.keys(expandedSchedules.value)) {
    expandedSchedules.value[id] = await api.listSchedules(id).catch(() => expandedSchedules.value[id])
  }
}

// runSamples maps a runId to its throughput series (MB/s) for the sparkline.
const runSamples = ref<Record<string, number[]>>({})

async function loadSamples(deploymentId: string, runs: Run[]) {
  for (const run of runs) {
    if (run.type !== 'initial-fill' && run.type !== 'incremental' && run.type !== 'verify') continue
    try {
      const s = await api.listRunSamples(deploymentId, run.id)
      runSamples.value = { ...runSamples.value, [run.id]: s.map((p) => p.mbps) }
    } catch {
      /* ignore */
    }
  }
}

// Poll while any job is in flight (deploy/teardown) or a fill is running
// (a visible VM still pending/working) so status and throughput flip live.
const busy = computed(() => {
  // A running job (deploy/teardown/power/fill) is the authoritative signal;
  // the per-VM check keeps live throughput flowing while a fill writes.
  if (deployments.value.some((d) => d.running || d.status === 'deploying' || d.status === 'deleting'))
    return true
  return Object.values(expandedVMs.value).some((vms) =>
    vms.some((vm) => vm.fillStatus === 'pending' || vm.fillStatus === 'working'),
  )
})
let timer: ReturnType<typeof setInterval> | undefined
onMounted(async () => {
  await reload().catch((e) => (error.value = String(e)))
  timer = setInterval(() => {
    if (busy.value) reload().catch(() => {})
  }, 2000)
})
onUnmounted(() => {
  clearInterval(timer)
  clearInterval(consoleTimer)
})

async function create() {
  error.value = ''
  try {
    const placement: Record<string, string> = {}
    for (const [k, v] of Object.entries(form.placement)) if (v) placement[k] = v
    // VMs are round-robined over these; the orchestrator splits on newline.
    if (datastores.value.length) placement.datastore = datastores.value.join('\n')
    await api.createDeployment({
      name: form.name,
      profileId: form.profileId,
      connectionId: form.connectionId,
      spec: adjustSpec.value && spec.value ? spec.value : undefined,
      placement,
    })
    creating.value = false
    form.name = ''
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

// Conflict warning: VMs whose names already exist on the hypervisor owned by
// another/unknown deployment. Resolved by an explicit adopt/clean/abort choice.
const conflictDialog = ref<{ deployment: Deployment; conflicts: VMConflict[] } | null>(null)

async function deploy(d: Deployment) {
  error.value = ''
  try {
    const conflicts = await api.deploymentConflicts(d.id)
    if (conflicts.length > 0) {
      conflictDialog.value = { deployment: d, conflicts } // ask before touching foreign VMs
      return
    }
    await api.deployDeployment(d.id)
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

// resolveConflicts runs the deploy with the operator's chosen resolution.
async function resolveConflicts(onConflict: 'adopt' | 'clean') {
  if (!conflictDialog.value) return
  const id = conflictDialog.value.deployment.id
  conflictDialog.value = null
  error.value = ''
  try {
    await api.deployDeployment(id, onConflict)
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

// openScaleUp loads the deployment's current effective config into an editor.
function openScaleUp(d: Deployment) {
  editing.value = d
  editSpec.value = { ...d.spec }
}

// saveAndReconcile persists the grown spec then runs the reconcile job,
// which creates the added VMs / disks (grow-only is enforced server-side).
async function saveAndReconcile() {
  if (!editing.value || !editSpec.value) return
  error.value = ''
  const d = editing.value
  try {
    await api.updateDeploymentSpec(d.id, editSpec.value)
    editing.value = null
    editSpec.value = null
    await deploy(d) // shared conflict-aware deploy path
  } catch (e) {
    error.value = String(e)
  }
}

async function teardown(d: Deployment) {
  if (!confirm(`Tear down "${d.name}"? All its VMs and disks are deleted; run history is kept.`))
    return
  error.value = ''
  try {
    await api.deleteDeployment(d.id)
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

async function power(d: Deployment, on: boolean) {
  error.value = ''
  try {
    await api.powerDeployment(d.id, on)
  } catch (e) {
    error.value = String(e)
  }
}

async function fill(d: Deployment, type: 'initial-fill' | 'incremental' | 'verify') {
  error.value = ''
  try {
    await api.fillDeployment(d.id, type)
    // Auto-open the VM panel so live per-VM progress is visible immediately.
    if (!expandedVMs.value[d.id]) await toggleVMs(d)
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

// --- discovered VMs (unknown machines that PXE-booted, e.g. backup restores) ---
const discovered = ref<DiscoveredVM[]>([])

// discoveredDisks parses a discovered VM's inspection report.
function discoveredDisks(dv: DiscoveredVM): DiscoveryDisk[] {
  if (!dv.inspection) return []
  try {
    return (JSON.parse(dv.inspection) as { disks?: DiscoveryDisk[] }).disks ?? []
  } catch {
    return []
  }
}

// discoveredIdentity returns the first GhostFleet identity marker found on a
// discovered VM's disks (adoption decisions key off it).
function discoveredIdentity(dv: DiscoveredVM) {
  return discoveredDisks(dv).find((d) => d.identity)?.identity
}

function discoveredSummary(dv: DiscoveredVM): string {
  const disks = discoveredDisks(dv)
  if (!dv.inspection) return 'waiting for disk inspection…'
  if (disks.length === 0) return 'no disks reported'
  const id = discoveredIdentity(dv)
  const parts = [`${disks.length} disk(s)`]
  if (id) {
    parts.push(`${id.vmName} of “${id.deploymentName}”`)
    const run = disks.find((d) => d.runId)?.runId
    if (run) parts.push(`last run ${run.slice(0, 8)}`)
    parts.push(disks.every((d) => !d.identity || d.manifest) ? 'verifiable ✓' : 'no manifest')
  } else {
    parts.push('no GhostFleet identity — not adoptable')
  }
  return parts.join(' · ')
}

// Adopt dialog state.
const adoptDialog = ref<DiscoveredVM | null>(null)
const adoptMode = ref<'rebind' | 'new-deployment'>('new-deployment')
const adoptName = ref('')
const adoptJoinId = ref('') // '' = create a new deployment
const adoptWarning = ref('')

// Existing adopted deployments the VM could join instead of creating one.
const adoptedDeployments = computed(() =>
  deployments.value.filter((d) => d.origin === 'adopted' && d.status === 'ready'),
)

function openAdopt(dv: DiscoveredVM) {
  const id = discoveredIdentity(dv)
  adoptDialog.value = dv
  adoptMode.value = 'new-deployment'
  adoptName.value = id ? `adopted-${id.vmName}` : ''
  adoptJoinId.value = ''
  adoptWarning.value = ''
}

async function adopt() {
  if (!adoptDialog.value) return
  error.value = ''
  try {
    const res = await api.adoptDiscoveredVM(adoptDialog.value.id, {
      mode: adoptMode.value,
      name: adoptMode.value === 'new-deployment' && !adoptJoinId.value ? adoptName.value : undefined,
      deploymentId: adoptMode.value === 'new-deployment' && adoptJoinId.value ? adoptJoinId.value : undefined,
    })
    adoptDialog.value = null
    if (res.warning) adoptWarning.value = res.warning
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

async function dismissDiscovered(dv: DiscoveredVM) {
  if (!confirm(`Dismiss discovered VM ${dv.mac}? It is re-discovered if it PXE-boots again.`)) return
  error.value = ''
  try {
    await api.deleteDiscoveredVM(dv.id)
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

// --- schedules (timer-based run kickoffs) ---

async function toggleSchedules(d: Deployment) {
  if (expandedSchedules.value[d.id]) {
    const copy = { ...expandedSchedules.value }
    delete copy[d.id]
    expandedSchedules.value = copy
    return
  }
  try {
    expandedSchedules.value = { ...expandedSchedules.value, [d.id]: await api.listSchedules(d.id) }
  } catch (e) {
    error.value = String(e)
  }
}

// Schedule dialog state. The three kinds map straight onto the API:
// every = Go duration, daily = HH:MM(@days) + timezone, once = RFC3339.
const scheduleDialog = ref<Deployment | null>(null)
// Non-null while the dialog edits an existing schedule instead of adding one.
const schedEditing = ref<Schedule | null>(null)
const schedForm = reactive({
  action: 'incremental' as Schedule['action'],
  kind: 'every' as Schedule['kind'],
  every: '24h',
  dailyTime: '06:00',
  dailyDays: [] as string[],
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone ?? '',
  onceLocal: '', // datetime-local value, converted to RFC3339 on submit
  enabled: true, // edit only; a new schedule always starts active
})
const weekdayOptions = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']

// The API only accepts IANA zone names, so the field offers the browser's zone
// database rather than free text. Each entry carries its current UTC offset
// ("Europe/Berlin (UTC+02:00)") to make the list easier to scan; the offset is
// display only, the stored value stays the zone name.
const timezoneOptions = ((): { value: string; label: string }[] => {
  const now = new Date()
  const offset = (tz: string): string => {
    try {
      const name = new Intl.DateTimeFormat('en-US', { timeZone: tz, timeZoneName: 'longOffset' })
        .formatToParts(now)
        .find((p) => p.type === 'timeZoneName')?.value
      if (!name) return ''
      return name === 'GMT' ? 'UTC+00:00' : name.replace('GMT', 'UTC')
    } catch {
      return '' // zone unknown to the browser, or no longOffset support
    }
  }
  const local = Intl.DateTimeFormat().resolvedOptions().timeZone ?? ''
  const all: string[] =
    typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : []
  // Pin UTC and the browser's own zone to the top; the rest stays alphabetical.
  const pinned = ['UTC', local].filter(Boolean)
  const rest = all.filter((tz) => !pinned.includes(tz)).sort((a, b) => a.localeCompare(b))
  return [...new Set([...pinned, ...rest])].map((tz) => {
    const off = offset(tz)
    return { value: tz, label: off ? `${tz} (${off})` : tz }
  })
})()

function closeScheduleDialog() {
  scheduleDialog.value = null
  schedEditing.value = null
  error.value = ''
}

// localInput renders an instant as a datetime-local value (local wall-clock).
function localInput(t: Date): string {
  const shifted = new Date(t.getTime() - t.getTimezoneOffset() * 60_000)
  return shifted.toISOString().slice(0, 16)
}

function openScheduleDialog(d: Deployment) {
  scheduleDialog.value = d
  schedEditing.value = null
  error.value = ''
  schedForm.action = 'incremental'
  schedForm.kind = 'every'
  schedForm.every = '24h'
  schedForm.dailyTime = '06:00'
  schedForm.dailyDays = []
  schedForm.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone ?? ''
  // Default the one-shot to 30 days out ("tear down in 30 days"), minute precision.
  schedForm.onceLocal = localInput(new Date(Date.now() + 30 * 24 * 3600 * 1000))
}

// openScheduleEdit reopens the same dialog against an existing schedule,
// unpacking its spec back into the per-kind fields. Fields the schedule does
// not use keep the defaults, so switching kind mid-edit still yields something
// sensible.
function openScheduleEdit(d: Deployment, sc: Schedule) {
  openScheduleDialog(d)
  schedEditing.value = sc
  schedForm.action = sc.action
  schedForm.kind = sc.kind
  // A spent one-shot was disabled by the scheduler, not by the user, so giving
  // it a new fire time here means re-arming it. Anything else keeps its state.
  schedForm.enabled = sc.enabled || (sc.kind === 'once' && !!sc.lastFiredAt)
  switch (sc.kind) {
    case 'every':
      schedForm.every = sc.spec
      break
    case 'daily': {
      const [time, days] = sc.spec.split('@')
      schedForm.dailyTime = time ?? '06:00'
      schedForm.dailyDays = days ? days.split(',') : []
      schedForm.timezone = sc.timezone ?? ''
      break
    }
    case 'once':
      schedForm.onceLocal = localInput(new Date(sc.spec))
      break
  }
}

// schedRequest builds the API body from the dialog's per-kind fields.
function schedRequest(): ScheduleRequest {
  switch (schedForm.kind) {
    case 'every':
      return { action: schedForm.action, kind: 'every', spec: schedForm.every }
    case 'daily':
      return {
        action: schedForm.action,
        kind: 'daily',
        spec: schedForm.dailyTime + (schedForm.dailyDays.length ? '@' + schedForm.dailyDays.join(',') : ''),
        timezone: schedForm.timezone || undefined,
      }
    case 'once':
      return {
        action: schedForm.action,
        kind: 'once',
        spec: new Date(schedForm.onceLocal).toISOString(),
      }
  }
}

// submitSchedule creates a new schedule, or replaces the definition of the one
// being edited. An edit keeps the paused/running state and resets the next fire
// time to whatever the new definition implies.
async function submitSchedule() {
  if (!scheduleDialog.value) return
  const d = scheduleDialog.value
  const editing = schedEditing.value
  // A timer that deletes the fleet deserves an explicit confirmation — on an
  // edit only when it is turning into a teardown.
  if (
    schedForm.action === 'teardown' &&
    editing?.action !== 'teardown' &&
    !confirm(`Schedule a teardown of "${d.name}"? When it fires, all its VMs and disks are deleted.`)
  )
    return
  error.value = ''
  try {
    if (editing) await api.updateSchedule(editing.id, { ...schedRequest(), enabled: schedForm.enabled })
    else await api.createSchedule(d.id, schedRequest())
    scheduleDialog.value = null
    schedEditing.value = null
    expandedSchedules.value = { ...expandedSchedules.value, [d.id]: await api.listSchedules(d.id) }
  } catch (e) {
    error.value = String(e)
  }
}

// toggleSchedule pauses/resumes by resending the definition with enabled flipped.
async function toggleSchedule(d: Deployment, sc: Schedule) {
  error.value = ''
  try {
    await api.updateSchedule(sc.id, {
      action: sc.action,
      kind: sc.kind,
      spec: sc.spec,
      timezone: sc.timezone,
      enabled: !sc.enabled,
    })
    expandedSchedules.value = { ...expandedSchedules.value, [d.id]: await api.listSchedules(d.id) }
  } catch (e) {
    error.value = String(e)
  }
}

async function removeSchedule(d: Deployment, sc: Schedule) {
  if (!confirm(`Delete the "${scheduleWhen(sc)}" schedule?`)) return
  error.value = ''
  try {
    await api.deleteSchedule(sc.id)
    expandedSchedules.value = { ...expandedSchedules.value, [d.id]: await api.listSchedules(d.id) }
  } catch (e) {
    error.value = String(e)
  }
}

const scheduleActionLabels: Record<Schedule['action'], string> = {
  incremental: 'Incremental run',
  verify: 'Verify data',
  'power-on': 'Power on',
  'power-off': 'Power off',
  teardown: 'Tear down',
}

// scheduleWhen renders the cadence compactly ("every 24h", "Mon,Fri at 06:00").
function scheduleWhen(sc: Schedule): string {
  switch (sc.kind) {
    case 'every':
      return `every ${sc.spec}`
    case 'daily': {
      const [time, days] = sc.spec.split('@')
      return `${days ? days : 'daily'} at ${time}${sc.timezone ? ` (${sc.timezone})` : ' (UTC)'}`
    }
    case 'once':
      return `once at ${fmtTime(sc.spec)}`
  }
}

// relTime renders a future timestamp as a countdown ("in 3h 12m").
function relTime(ts?: string): string {
  if (!ts) return '—'
  let sec = Math.round((new Date(ts).getTime() - Date.now()) / 1000)
  if (sec <= 0) return 'now'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `in ${d}d ${h}h`
  if (h > 0) return `in ${h}h ${m}m`
  return `in ${Math.max(1, m)}m`
}

// cancelJob aborts whatever job is currently running for the deployment
// (deploy/scale-up/teardown/power/fill).
async function cancelJob(d: Deployment) {
  if (!confirm(`Cancel the running job for "${d.name}"?`)) return
  error.value = ''
  try {
    await api.cancelDeployment(d.id)
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

function fillBar(vm: ManagedVM): { pct: number; label: string; cls: string } {
  const pct = vm.bytesTotal > 0 ? Math.min(100, (vm.bytesWritten / vm.bytesTotal) * 100) : 0
  const gib = (n: number) => (n / 1024 ** 3).toFixed(1)
  switch (vm.fillStatus) {
    case 'working':
      return {
        pct,
        label: `${pct.toFixed(0)}% · ${gib(vm.bytesWritten)}/${gib(vm.bytesTotal)} GiB · ${vm.mbps.toFixed(0)} MB/s`,
        cls: 'bg-sky-500',
      }
    case 'done':
      return { pct: 100, label: 'done', cls: 'bg-emerald-500' }
    case 'failed':
      return { pct, label: vm.fillError || 'failed', cls: 'bg-red-500' }
    case 'pending':
      return { pct: 0, label: 'queued', cls: 'bg-slate-300' }
    default:
      return { pct: 0, label: '', cls: '' }
  }
}

function agentBadge(vm: ManagedVM): { label: string; cls: string } {
  if (vm.agentStatus !== 'online' || !vm.agentSeenAt)
    return { label: 'no agent', cls: 'text-slate-400' }
  const ageSec = (Date.now() - new Date(vm.agentSeenAt).getTime()) / 1000
  return ageSec < 30
    ? { label: 'agent online', cls: 'text-emerald-600' }
    : { label: `agent lost (${Math.round(ageSec / 60)}m)`, cls: 'text-amber-600' }
}

async function toggleVMs(d: Deployment) {
  if (expandedVMs.value[d.id]) {
    const copy = { ...expandedVMs.value }
    delete copy[d.id]
    expandedVMs.value = copy
    return
  }
  try {
    expandedVMs.value = { ...expandedVMs.value, [d.id]: await api.listDeploymentVMs(d.id) }
  } catch (e) {
    error.value = String(e)
  }
}

// Console viewer modal state.
const consoleVM = ref<ManagedVM | null>(null)
const consoleText = ref('')
const consoleLoading = ref(false)
const consoleSessionCount = ref(0)
// 0 = follow the newest boot (stays 0, so a boot that starts while the viewer
// is open is picked up instead of pinning the view to the boot it opened on).
const consoleSession = ref(0)
const consoleSessions = ref<{ n: number; startedAt: string; partial: boolean }[]>([])

function sessionLabel(s: { n: number; startedAt: string; partial: boolean }): string {
  const when = s.startedAt ? new Date(s.startedAt + 'Z').toLocaleString() : 'time unknown'
  const tag = s.partial ? ' (partial)' : s.n === consoleSessionCount.value ? ' (latest)' : ''
  return `boot ${s.n} — ${when}${tag}`
}
const consolePre = ref<HTMLElement | null>(null)
let consoleTimer: ReturnType<typeof setInterval> | undefined

// True once a poll has returned real console content, so transient empty/
// error responses (e.g. a slow read while a fill loads vCenter) keep the
// last-good text on screen instead of blanking it.
const consoleHasContent = ref(false)
let consoleInFlight = false

async function openConsole(vm: ManagedVM) {
  consoleVM.value = vm
  consoleText.value = ''
  consoleHasContent.value = false
  consoleSession.value = 0 // follow latest on open
  await refreshConsole(true)
  // Live-tail while open.
  clearInterval(consoleTimer)
  consoleTimer = setInterval(() => refreshConsole(false), 4000)
}

// scrollToEnd is true when we should jump to the bottom (open, session
// change, or while following the latest session).
async function refreshConsole(scrollToEnd: boolean) {
  if (!consoleVM.value) return
  if (consoleInFlight) return // don't stack polls when a read is slow
  consoleInFlight = true
  consoleLoading.value = true
  try {
    const r = await api.getVMConsole(consoleVM.value.deploymentId, consoleVM.value.id, consoleSession.value)
    // Only adopt a session list that has something in it. A read that comes
    // back with no sessions (a log truncated on power-on, a tail that landed
    // in NUL padding) must not wipe the boots we already know about — that
    // took the picker away exactly when the current boot was empty and an
    // earlier one was the only thing worth reading.
    if (r.sessionCount > 0) {
      consoleSessionCount.value = r.sessionCount
      consoleSessions.value = r.sessions
    }
    const following = consoleSession.value === 0
    if (r.console.trim()) {
      consoleText.value = r.console
      consoleHasContent.value = true
    } else if (!consoleHasContent.value) {
      // A run that has just started has an empty (or freshly restarted) log.
      // Say so, and point at the earlier boots the picker still offers —
      // judged on what the picker holds, not on this read alone.
      consoleText.value =
        consoleSessionCount.value > 1
          ? '(this boot has no output yet — the VM may still be booting; pick an earlier boot above to read a previous run)'
          : '(no console output yet — the VM may still be booting)'
    }
    // Jump to end on open/session-change, and keep following the newest boot.
    if (scrollToEnd || following || consoleSession.value === r.sessionCount) {
      await nextTick()
      if (consolePre.value) consolePre.value.scrollTop = consolePre.value.scrollHeight
    }
  } catch (e) {
    // Keep the last-good log if we have one — a fill loads vCenter and can
    // make a read time out; the next poll recovers. Only surface the error
    // when there is nothing on screen yet.
    if (!consoleHasContent.value) {
      consoleText.value = `Console temporarily unavailable (vCenter busy during a run?) — retrying…\n\n${e}`
    }
  } finally {
    consoleLoading.value = false
    consoleInFlight = false
  }
}

function selectSession(n: number) {
  consoleSession.value = n
  refreshConsole(true)
}

function closeConsole() {
  clearInterval(consoleTimer)
  consoleVM.value = null
}

async function toggleRuns(d: Deployment) {
  if (expandedRuns.value[d.id]) {
    const copy = { ...expandedRuns.value }
    delete copy[d.id]
    expandedRuns.value = copy
    return
  }
  try {
    const runs = await api.listRuns(d.id)
    expandedRuns.value = { ...expandedRuns.value, [d.id]: runs }
    await loadSamples(d.id, runs)
  } catch (e) {
    error.value = String(e)
  }
}

// --- API endpoint reference (copy-pastable URLs for scripting) ---
const expandedApi = ref<Record<string, boolean>>({})
const copied = ref('')

function toggleApi(d: Deployment) {
  expandedApi.value = { ...expandedApi.value, [d.id]: !expandedApi.value[d.id] }
}

// apiEndpoints lists every deployment action as a method + absolute URL (+ JSON
// body where one is needed), so they can be copied straight into a script.
function apiEndpoints(d: Deployment): { action: string; method: string; url: string; body?: string }[] {
  const u = `${window.location.origin}/api/v1/deployments/${d.id}`
  return [
    { action: 'Incremental run', method: 'POST', url: `${u}/fill`, body: '{"type":"incremental"}' },
    { action: 'Initial fill', method: 'POST', url: `${u}/fill`, body: '{"type":"initial-fill"}' },
    { action: 'Verify data', method: 'POST', url: `${u}/fill`, body: '{"type":"verify"}' },
    { action: 'Deploy / reconcile', method: 'POST', url: `${u}/deploy`, body: '{"onConflict":"abort"}' },
    { action: 'Cancel running job', method: 'POST', url: `${u}/cancel` },
    { action: 'Power on', method: 'POST', url: `${u}/power`, body: '{"on":true}' },
    { action: 'Power off', method: 'POST', url: `${u}/power`, body: '{"on":false}' },
    { action: 'Conflicts (preflight)', method: 'GET', url: `${u}/conflicts` },
    { action: 'Schedules', method: 'GET', url: `${u}/schedules` },
    {
      action: 'Add schedule',
      method: 'POST',
      url: `${u}/schedules`,
      body: '{"action":"incremental","kind":"every","spec":"24h"}',
    },
    { action: 'Runs (history)', method: 'GET', url: `${u}/runs` },
    { action: 'VMs', method: 'GET', url: `${u}/vms` },
    { action: 'Update spec (scale-up)', method: 'PUT', url: `${u}/spec`, body: '{"spec":{ … }}' },
    { action: 'Tear down', method: 'DELETE', url: u },
  ]
}

async function copyText(text: string, label: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = label
    setTimeout(() => {
      if (copied.value === label) copied.value = ''
    }, 1200)
  } catch {
    /* clipboard blocked (e.g. non-secure context) — selecting the text still works */
  }
}

// runDuration formats a run's elapsed time.
function runDuration(run: Run): string {
  if (!run.startedAt) return '—'
  const end = run.finishedAt ? new Date(run.finishedAt) : new Date()
  const sec = Math.max(0, (end.getTime() - new Date(run.startedAt).getTime()) / 1000)
  if (sec < 90) return `${sec.toFixed(0)}s`
  return `${(sec / 60).toFixed(1)}m`
}

// runError pulls the human-readable error out of a failed run's stats JSON.
function runError(run: Run): string {
  if (run.status !== 'failed' || !run.stats) return ''
  try {
    return (JSON.parse(run.stats) as { error?: string }).error ?? ''
  } catch {
    return run.stats
  }
}

// runSummary renders a failed run's error or a succeeded run's stats compactly.
function runSummary(run: Run): string {
  const err = runError(run)
  if (err) return err
  if (!run.stats) return ''
  try {
    const s = JSON.parse(run.stats) as Record<string, number>
    return Object.entries(s)
      .filter(([k]) => k !== 'durationSec')
      .map(([k, v]) => `${k}=${v}`)
      .join(' ')
  } catch {
    return run.stats
  }
}

function fmtTime(ts?: string): string {
  return ts ? new Date(ts).toLocaleString() : '—'
}

const runStatusClass: Record<string, string> = {
  succeeded: 'text-emerald-600',
  failed: 'text-red-600',
  cancelled: 'text-slate-500',
  running: 'text-amber-600',
  pending: 'text-slate-400',
}

const statusClass: Record<string, string> = {
  new: 'bg-slate-100 text-slate-600',
  deploying: 'bg-amber-100 text-amber-700 animate-pulse',
  ready: 'bg-emerald-100 text-emerald-700',
  error: 'bg-red-100 text-red-700',
  deleting: 'bg-amber-100 text-amber-700 animate-pulse',
  deleted: 'bg-slate-100 text-slate-400 line-through',
}

const fieldClass =
  'w-full rounded-lg border border-slate-300 px-3 py-1.5 text-sm focus:border-slate-500 focus:outline-none'
const labelClass = 'block text-xs font-medium text-slate-500 mb-1'
</script>

<template>
  <div>
    <!-- Cross-deployment conflict warning: target VM names already on the
         hypervisor owned by another/unknown deployment. -->
    <div
      v-if="conflictDialog"
      class="fixed inset-0 z-30 flex items-center justify-center bg-black/40 p-4"
      @click.self="conflictDialog = null"
    >
      <div class="w-full max-w-2xl rounded-xl bg-white p-6 shadow-xl">
        <h3 class="mb-2 text-lg font-semibold text-amber-700">VMs already exist</h3>
        <p class="mb-3 text-sm text-slate-600">
          Deploying <strong>{{ conflictDialog.deployment.name }}</strong> would touch
          {{ conflictDialog.conflicts.length }} VM(s) that already exist on the hypervisor
          and belong to another (or an unknown) deployment. Choose how to proceed.
        </p>
        <div class="mb-4 max-h-56 overflow-auto rounded-lg border border-slate-200">
          <table class="w-full text-xs">
            <thead class="bg-slate-50 text-left text-slate-400">
              <tr>
                <th class="px-3 py-1.5">VM</th>
                <th class="px-3 py-1.5">Owner</th>
                <th class="px-3 py-1.5">Created</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in conflictDialog.conflicts" :key="c.ref" class="border-t border-slate-100">
                <td class="px-3 py-1.5 font-mono">{{ c.name }}</td>
                <td class="px-3 py-1.5">
                  {{ c.ownerName || (c.ownerId ? 'deployment ' + c.ownerId.slice(0, 8) : 'unknown') }}
                  <span v-if="c.ownerId && !c.ownerKnown" class="text-slate-400">(not on this controller)</span>
                </td>
                <td class="px-3 py-1.5 text-slate-400">{{ fmtTime(c.ownerSince) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="flex justify-end gap-2">
          <button
            class="rounded-lg px-4 py-2 text-sm font-medium text-slate-600 hover:bg-slate-100"
            @click="conflictDialog = null"
          >
            Abort
          </button>
          <button
            class="rounded-lg border border-slate-300 px-4 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
            @click="resolveConflicts('adopt')"
          >
            Adopt into this deployment
          </button>
          <button
            class="rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700"
            @click="resolveConflicts('clean')"
          >
            Delete &amp; re-create
          </button>
        </div>
      </div>
    </div>

    <!-- Adopt dialog: turn a discovered VM (e.g. a backup restore) back into
         a managed one — in place (rebind) or as a new adopted deployment. -->
    <div
      v-if="adoptDialog"
      class="fixed inset-0 z-30 flex items-center justify-center bg-black/40 p-4"
      @click.self="adoptDialog = null"
    >
      <div class="w-full max-w-xl rounded-xl bg-white p-6 shadow-xl">
        <h3 class="mb-2 text-lg font-semibold">Adopt discovered VM</h3>
        <p class="mb-4 text-sm text-slate-600">
          <span class="font-mono">{{ adoptDialog.mac }}</span> —
          {{ discoveredSummary(adoptDialog) }}
        </p>
        <div class="space-y-3 text-sm">
          <label class="flex items-start gap-2">
            <input v-model="adoptMode" type="radio" value="new-deployment" class="mt-1" />
            <span>
              <strong>Adopt into a new deployment</strong> (restore-alongside) — the original
              deployment stays untouched; run verify/incrementals on the copy, then tear it down.
              <span class="mt-2 block space-y-2">
                <select
                  v-if="adoptedDeployments.length"
                  v-model="adoptJoinId"
                  :class="fieldClass"
                  :disabled="adoptMode !== 'new-deployment'"
                >
                  <option value="">Create new deployment…</option>
                  <option v-for="a in adoptedDeployments" :key="a.id" :value="a.id">
                    Join “{{ a.name }}”
                  </option>
                </select>
                <input
                  v-if="!adoptJoinId"
                  v-model="adoptName"
                  :class="fieldClass"
                  :disabled="adoptMode !== 'new-deployment'"
                  placeholder="deployment name"
                />
              </span>
            </span>
          </label>
          <label class="flex items-start gap-2">
            <input v-model="adoptMode" type="radio" value="rebind" class="mt-1" />
            <span>
              <strong>Rebind into its original deployment</strong> (restore-in-place) — the VM takes
              its original record's place
              <template v-if="discoveredIdentity(adoptDialog)">
                (“{{ discoveredIdentity(adoptDialog)!.deploymentName }}” /
                {{ discoveredIdentity(adoptDialog)!.vmName }})</template
              >. If the original VM still exists it becomes unmanaged.
            </span>
          </label>
        </div>
        <div class="mt-6 flex justify-end gap-2">
          <button
            class="rounded-lg px-4 py-2 text-sm font-medium text-slate-600 hover:bg-slate-100"
            @click="adoptDialog = null"
          >
            Cancel
          </button>
          <button
            class="rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
            @click="adopt"
          >
            Adopt
          </button>
        </div>
      </div>
    </div>

    <!-- Schedule dialog: fire one run type on a timer (every / daily / once). -->
    <div
      v-if="scheduleDialog"
      class="fixed inset-0 z-30 flex items-center justify-center bg-black/40 p-4"
      @click.self="closeScheduleDialog"
    >
      <div class="w-full max-w-xl rounded-xl bg-white p-6 shadow-xl">
        <h3 class="mb-1 text-lg font-semibold">
          {{ schedEditing ? 'Edit schedule' : 'Add schedule' }}
        </h3>
        <p v-if="schedEditing" class="mb-4 text-sm text-slate-600">
          Change when and what fires on <strong>{{ scheduleDialog.name }}</strong>. Saving
          recomputes the next fire time; the fire history is kept.
        </p>
        <p v-else class="mb-4 text-sm text-slate-600">
          Run an action on <strong>{{ scheduleDialog.name }}</strong> automatically — e.g. an
          incremental every 24 h, power on daily at 06:00, or a teardown in 30 days.
        </p>
        <div class="space-y-4 text-sm">
          <div>
            <label :class="labelClass">Action</label>
            <select v-model="schedForm.action" :class="fieldClass">
              <option
                v-for="(label, action) in scheduleActionLabels"
                :key="action"
                :value="action"
              >
                {{ label }}
              </option>
            </select>
            <p
              v-if="(schedForm.action === 'incremental' || schedForm.action === 'verify') &&
                !scheduleDialog.filled && scheduleDialog.origin !== 'adopted'"
              class="mt-1 text-xs text-amber-600"
            >
              Needs a succeeded initial fill — fires before one exists are recorded as errors.
            </p>
          </div>
          <div>
            <label :class="labelClass">When</label>
            <div class="flex gap-4">
              <label class="flex items-center gap-1.5">
                <input v-model="schedForm.kind" type="radio" value="every" /> Every interval
              </label>
              <label class="flex items-center gap-1.5">
                <input v-model="schedForm.kind" type="radio" value="daily" /> Daily at a time
              </label>
              <label class="flex items-center gap-1.5">
                <input v-model="schedForm.kind" type="radio" value="once" /> Once
              </label>
            </div>
          </div>
          <div v-if="schedForm.kind === 'every'">
            <label :class="labelClass">Interval</label>
            <input v-model="schedForm.every" :class="fieldClass" placeholder="24h" />
            <p class="mt-1 text-xs text-slate-400">
              Go duration, e.g. <code>24h</code>, <code>90m</code>, <code>7h30m</code>. First fire
              one interval from now.
            </p>
          </div>
          <div v-else-if="schedForm.kind === 'daily'" class="grid grid-cols-2 gap-4">
            <div>
              <label :class="labelClass">Time</label>
              <input v-model="schedForm.dailyTime" type="time" :class="fieldClass" />
            </div>
            <div>
              <label :class="labelClass">Timezone</label>
              <SearchableSelect
                v-model="schedForm.timezone"
                :options="timezoneOptions"
                placeholder="UTC"
              />
              <p class="mt-1 text-xs text-slate-400">Search by city, e.g. “Berlin”.</p>
            </div>
            <div class="col-span-2">
              <label :class="labelClass">Only on (empty = every day)</label>
              <MultiSelect v-model="schedForm.dailyDays" :options="weekdayOptions" />
            </div>
          </div>
          <div v-else>
            <label :class="labelClass">Fire at</label>
            <input v-model="schedForm.onceLocal" type="datetime-local" :class="fieldClass" />
            <p class="mt-1 text-xs text-slate-400">
              Local time; fires once, then the schedule disables itself.
            </p>
          </div>
          <div v-if="schedEditing">
            <label class="flex items-center gap-1.5">
              <input v-model="schedForm.enabled" type="checkbox" /> Active
            </label>
            <p v-if="!schedForm.enabled" class="mt-1 text-xs text-slate-400">
              Saved as paused — it will not fire until resumed.
            </p>
          </div>
        </div>
        <!-- The page-level error banner sits behind this overlay, so a rejected
             definition (bad duration, a one-shot in the past) is repeated here. -->
        <p v-if="error" class="mt-4 text-sm text-red-600">{{ error }}</p>
        <div class="mt-6 flex justify-end gap-2">
          <button
            class="rounded-lg px-4 py-2 text-sm font-medium text-slate-600 hover:bg-slate-100"
            @click="closeScheduleDialog"
          >
            Cancel
          </button>
          <button
            class="rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
            @click="submitSchedule"
          >
            {{ schedEditing ? 'Save changes' : 'Add schedule' }}
          </button>
        </div>
      </div>
    </div>

    <div class="mb-6 flex items-center justify-between">
      <h2 class="text-xl font-semibold">Deployments</h2>
      <button
        v-if="!creating"
        class="rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
        @click="creating = true"
      >
        New deployment
      </button>
    </div>

    <p v-if="error" class="mb-4 text-sm text-red-600">{{ error }}</p>
    <p
      v-if="adoptWarning"
      class="mb-4 rounded-lg border border-amber-200 bg-amber-50 px-4 py-2 text-sm text-amber-800"
    >
      {{ adoptWarning }}
      <button class="ml-2 underline" @click="adoptWarning = ''">dismiss</button>
    </p>

    <!-- Discovered VMs: unknown machines that PXE-booted on the isolated
         network — typically backup restores of ghost VMs (fresh MAC). -->
    <div
      v-if="discovered.some((dv) => dv.status === 'new')"
      class="mb-6 rounded-xl border border-sky-200 bg-sky-50 p-4"
    >
      <h3 class="mb-2 text-sm font-semibold text-sky-900">Discovered VMs</h3>
      <p class="mb-3 text-xs text-sky-800">
        Unknown machines booted on the isolated network — typically restored ghost VMs (a restore
        comes up with a fresh MAC). Adopt one to verify the restore or run incrementals on it.
      </p>
      <div class="space-y-1.5">
        <template v-for="dv in discovered" :key="dv.id">
          <div v-if="dv.status === 'new'" class="flex items-center gap-3 text-xs">
            <span class="w-36 shrink-0 font-mono text-slate-700">{{ dv.mac }}</span>
            <span class="flex-1 text-slate-600">{{ discoveredSummary(dv) }}</span>
            <span class="shrink-0 text-slate-400">seen {{ fmtTime(dv.lastSeenAt) }}</span>
            <button
              v-if="discoveredIdentity(dv)"
              class="shrink-0 font-medium text-sky-700 hover:text-sky-900"
              @click="openAdopt(dv)"
            >
              Adopt…
            </button>
            <button class="shrink-0 text-slate-400 hover:text-slate-600" @click="dismissDiscovered(dv)">
              Dismiss
            </button>
          </div>
        </template>
      </div>
    </div>

    <!-- Create form: profile template + connection + placement + optional overrides (PRO-8/9) -->
    <form v-if="creating" class="mb-6 rounded-xl border border-slate-200 bg-white p-6" @submit.prevent="create">
      <h3 class="mb-4 font-medium">New deployment</h3>
      <div class="grid grid-cols-2 gap-4 lg:grid-cols-3">
        <div>
          <label :class="labelClass">Deployment name</label>
          <input v-model="form.name" :class="fieldClass" required placeholder="e.g. backup-test-q3" />
        </div>
        <div>
          <label :class="labelClass">Profile (template)</label>
          <select v-model="form.profileId" :class="fieldClass" required>
            <option disabled value="">Select…</option>
            <option v-for="p in profiles" :key="p.id" :value="p.id">
              {{ p.name }} (v{{ p.currentVersion }})
            </option>
          </select>
        </div>
        <div>
          <label :class="labelClass">Connection</label>
          <select v-model="form.connectionId" :class="fieldClass" required>
            <option disabled value="">Select…</option>
            <option v-for="c in connections" :key="c.id" :value="c.id">{{ c.name }}</option>
          </select>
        </div>
      </div>

      <p v-if="placementError" class="mt-4 text-sm text-red-600">{{ placementError }}</p>
      <template v-if="placementOptions">
        <h4 class="mt-6 mb-2 text-xs font-semibold uppercase tracking-wide text-slate-400">
          Placement
        </h4>
        <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <div v-if="placementOptions.datacenters?.length">
            <label :class="labelClass">Datacenter</label>
            <SearchableSelect v-model="form.placement.datacenter" :options="placementOptions.datacenters" />
          </div>
          <div>
            <label :class="labelClass">Cluster</label>
            <SearchableSelect v-model="form.placement.cluster" :options="placementOptions.clusters" />
          </div>
          <div v-if="clusterHosts.length">
            <label :class="labelClass">Host (pin VMs)</label>
            <SearchableSelect
              v-model="form.placement.host"
              :options="clusterHosts"
              empty-label="Cluster decides (DRS)"
            />
          </div>
          <div v-if="placementOptions.resourcePools?.length">
            <label :class="labelClass">Resource pool</label>
            <SearchableSelect
              v-model="form.placement.resourcePool"
              :options="placementOptions.resourcePools"
              empty-label="Cluster default"
            />
          </div>
          <div>
            <label :class="labelClass">Datastore(s)</label>
            <MultiSelect v-model="datastores" :options="datastoreNames" :hints="datastoreHints" />
            <p v-if="datastores.length > 1" class="mt-1 text-xs text-slate-400">
              VMs are spread round-robin across {{ datastores.length }} datastores.
            </p>
          </div>
          <div>
            <label :class="labelClass">Network</label>
            <SearchableSelect v-model="form.placement.network" :options="placementOptions.networks" />
          </div>
          <div>
            <label :class="labelClass">VM folder (optional)</label>
            <SearchableSelect
              v-model="form.placement.folder"
              :options="placementOptions.folders ?? []"
              empty-label='Default ("ghostfleet", auto-created)'
              :allow-custom="true"
              placeholder="pick or type a folder path"
            />
          </div>
        </div>
      </template>

      <div v-if="spec" class="mt-6">
        <label class="flex items-center gap-2 text-sm text-slate-600">
          <input v-model="adjustSpec" type="checkbox" class="rounded" />
          Adjust profile settings for this deployment (deploy-time overrides)
        </label>
        <div v-if="adjustSpec" class="mt-4 rounded-lg border border-slate-100 bg-slate-50 p-4">
          <SpecForm :spec="spec" />
        </div>
      </div>

      <div class="mt-6 flex gap-3">
        <button
          type="submit"
          :disabled="!placementOptions"
          class="rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700 disabled:cursor-not-allowed disabled:bg-slate-300"
        >
          Create deployment
        </button>
        <button
          type="button"
          class="rounded-lg border border-slate-300 px-4 py-2 text-sm hover:bg-slate-100"
          @click="creating = false"
        >
          Cancel
        </button>
      </div>
    </form>

    <!-- Console viewer modal (live-tails the VM's serial console.log) -->
    <div
      v-if="consoleVM"
      class="fixed inset-0 z-20 flex items-center justify-center bg-black/40 p-6"
      @click.self="closeConsole"
    >
      <div class="flex max-h-[80vh] w-full max-w-4xl flex-col rounded-xl bg-white shadow-xl">
        <div class="flex items-center justify-between border-b border-slate-200 px-5 py-3">
          <h3 class="flex items-center gap-3 font-medium">
            Console — {{ consoleVM.name }}
            <select
              v-if="consoleSessionCount > 0"
              :value="consoleSession"
              class="rounded border border-slate-300 px-2 py-0.5 text-xs font-normal"
              @change="selectSession(Number(($event.target as HTMLSelectElement).value))"
            >
              <option :value="0">follow latest boot</option>
              <option v-for="s in consoleSessions" :key="s.n" :value="s.n">
                {{ sessionLabel(s) }}
              </option>
            </select>
            <span v-if="consoleLoading" class="text-xs text-slate-400">refreshing…</span>
          </h3>
          <button class="text-slate-400 hover:text-slate-700" @click="closeConsole">✕</button>
        </div>
        <pre
          ref="consolePre"
          class="flex-1 overflow-auto bg-slate-900 p-4 font-mono text-xs leading-relaxed text-slate-100"
        >{{ consoleText }}</pre>
        <div class="border-t border-slate-100 px-5 py-2 text-right text-xs text-slate-400">
          per-boot session from the VM's serial console
        </div>
      </div>
    </div>

    <!-- Scale-up editor: grow the deployment's own effective config (PRO-6) -->
    <div
      v-if="editing && editSpec"
      class="mb-6 rounded-xl border border-slate-200 bg-white p-6"
    >
      <h3 class="mb-1 font-medium">Scale up “{{ editing.name }}”</h3>
      <p class="mb-4 text-sm text-slate-500">
        Grow this deployment's configuration, then reconcile to create the added
        VMs and disks. Values can only increase — shrinking is rejected; tear down
        to remove VMs.
      </p>
      <SpecForm :spec="editSpec" />
      <div class="mt-6 flex gap-3">
        <button
          class="rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
          @click="saveAndReconcile"
        >
          Save &amp; reconcile
        </button>
        <button
          class="rounded-lg border border-slate-300 px-4 py-2 text-sm hover:bg-slate-100"
          @click="((editing = null), (editSpec = null))"
        >
          Cancel
        </button>
      </div>
    </div>

    <div class="overflow-hidden rounded-xl border border-slate-200 bg-white">
      <table class="w-full text-sm">
        <thead class="bg-slate-50 text-left text-xs uppercase tracking-wide text-slate-400">
          <tr>
            <th class="px-4 py-3">Name</th>
            <th class="px-4 py-3">Status</th>
            <th class="px-4 py-3">VMs</th>
            <th class="px-4 py-3">Data total</th>
            <th class="px-4 py-3">Profile</th>
            <th class="px-4 py-3"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="deployments.length === 0">
            <td colspan="6" class="px-4 py-8 text-center text-slate-400">No deployments yet.</td>
          </tr>
          <template v-for="d in deployments" :key="d.id">
            <tr class="border-t border-slate-100">
              <td class="px-4 py-3 font-medium">
                {{ d.name }}
                <span
                  v-if="d.origin === 'adopted'"
                  class="ml-1 rounded-full bg-sky-100 px-2 py-0.5 text-xs font-normal text-sky-700"
                  title="Built from discovered VMs (e.g. a backup restore); not reconciled from a fleet shape"
                >
                  adopted
                </span>
              </td>
              <td class="px-4 py-3">
                <span class="rounded-full px-2 py-0.5 text-xs" :class="statusClass[d.status]">
                  {{ d.status }}
                </span>
              </td>
              <td class="px-4 py-3">
                <button class="text-slate-700 underline decoration-dotted" @click="toggleVMs(d)">
                  {{ d.spec.vmCount }}
                </button>
              </td>
              <td class="px-4 py-3">
                {{ d.spec.vmCount * d.spec.disksPerVM * d.spec.dataPerDiskGiB }} GiB
              </td>
              <td class="px-4 py-3 text-slate-500">
                {{ d.profileVersion ? `v${d.profileVersion}` : '—' }}
              </td>
              <td class="px-4 py-3 text-right whitespace-nowrap">
                <button
                  class="text-slate-500 hover:text-slate-900"
                  :class="{ 'font-medium text-red-500': d.status === 'error' }"
                  @click="toggleRuns(d)"
                >
                  {{ d.status === 'error' ? 'Why?' : 'Runs' }}
                </button>
                <button class="ml-3 text-slate-500 hover:text-slate-900" @click="toggleApi(d)">API</button>
                <button
                  v-if="d.status !== 'deleted' && d.status !== 'deleting'"
                  class="ml-3 text-slate-500 hover:text-slate-900"
                  @click="toggleSchedules(d)"
                >
                  Schedules
                </button>
                <!-- Adopted deployments are never reconciled (their VMs joined
                     via adoption) and never re-filled (that would wipe the
                     restored data) — verify/incremental/power/teardown apply. -->
                <button
                  v-if="d.origin !== 'adopted' && (d.status === 'new' || d.status === 'error' || d.status === 'ready')"
                  class="ml-3 text-emerald-600 hover:text-emerald-800"
                  @click="deploy(d)"
                >
                  {{ d.status === 'error' ? 'Retry' : d.status === 'ready' ? 'Reconcile' : 'Deploy' }}
                </button>
                <template v-if="d.status === 'ready'">
                  <button
                    v-if="d.origin !== 'adopted'"
                    class="ml-3 text-sky-600 hover:text-sky-800"
                    @click="fill(d, 'initial-fill')"
                  >
                    Fill
                  </button>
                  <!-- Incremental/verify need existing data: a prior fill, or
                       adopted (restored) disks. -->
                  <button
                    v-if="d.filled || d.origin === 'adopted'"
                    class="ml-3 text-sky-600 hover:text-sky-800"
                    @click="fill(d, 'incremental')"
                  >
                    Incremental
                  </button>
                  <button
                    v-if="d.filled || d.origin === 'adopted'"
                    class="ml-3 text-violet-600 hover:text-violet-800"
                    title="Read all generated data back and check it against the on-disk checksum manifests"
                    @click="fill(d, 'verify')"
                  >
                    Verify
                  </button>
                  <button
                    v-if="d.origin !== 'adopted'"
                    class="ml-3 text-slate-500 hover:text-slate-900"
                    @click="openScaleUp(d)"
                  >
                    Scale up
                  </button>
                  <button class="ml-3 text-slate-500 hover:text-slate-900" @click="power(d, true)">
                    Power on
                  </button>
                  <button class="ml-3 text-slate-500 hover:text-slate-900" @click="power(d, false)">
                    Power off
                  </button>
                </template>
                <button
                  v-if="d.running"
                  class="ml-3 text-orange-600 hover:text-orange-800"
                  @click="cancelJob(d)"
                >
                  Cancel
                </button>
                <button
                  v-if="d.status !== 'deleted' && d.status !== 'deleting'"
                  class="ml-3 text-red-400 hover:text-red-600"
                  @click="teardown(d)"
                >
                  Tear down
                </button>
              </td>
            </tr>
            <tr v-if="expandedVMs[d.id]" class="border-t border-slate-50 bg-slate-50">
              <td colspan="6" class="px-6 py-3">
                <p v-if="expandedVMs[d.id]!.length === 0" class="text-xs text-slate-400">
                  No VMs on the hypervisor (yet).
                </p>
                <div v-else class="space-y-1.5">
                  <div
                    v-for="vm in expandedVMs[d.id]"
                    :key="vm.id"
                    class="flex items-center gap-3 text-xs"
                  >
                    <span class="w-40 shrink-0 font-mono text-slate-600">{{ vm.name }}</span>
                    <span class="w-28 shrink-0 text-slate-500"
                      >{{ vm.diskCount }}×{{ vm.diskSizeGiB }} GiB</span
                    >
                    <span class="w-24 shrink-0" :class="agentBadge(vm).cls">{{
                      agentBadge(vm).label
                    }}</span>
                    <button
                      class="shrink-0 text-slate-400 hover:text-slate-700"
                      title="View serial console"
                      @click="openConsole(vm)"
                    >
                      console
                    </button>
                    <!-- Fill progress bar (when a run has touched this VM) -->
                    <div v-if="vm.fillStatus" class="flex flex-1 items-center gap-2">
                      <div class="h-2 flex-1 overflow-hidden rounded-full bg-slate-200">
                        <div
                          class="h-full transition-all"
                          :class="fillBar(vm).cls"
                          :style="{ width: fillBar(vm).pct + '%' }"
                        />
                      </div>
                      <span class="w-56 shrink-0 font-mono text-slate-500">{{ fillBar(vm).label }}</span>
                    </div>
                  </div>
                </div>
              </td>
            </tr>
            <tr v-if="expandedSchedules[d.id]" class="border-t border-slate-50 bg-slate-50">
              <td colspan="6" class="px-6 py-3">
                <div class="mb-2 flex items-center justify-between">
                  <p class="text-xs text-slate-500">
                    Timer-based runs. Fires while another job is running are retried for up to an
                    hour, then skipped; a fire missed while the controller was down is caught up
                    once on start.
                  </p>
                  <button
                    class="shrink-0 text-xs font-medium text-sky-600 hover:text-sky-800"
                    @click="openScheduleDialog(d)"
                  >
                    Add schedule…
                  </button>
                </div>
                <p v-if="expandedSchedules[d.id]!.length === 0" class="text-xs text-slate-400">
                  No schedules yet.
                </p>
                <table v-else class="w-full text-xs">
                  <tbody>
                    <tr v-for="sc in expandedSchedules[d.id]" :key="sc.id" class="align-top">
                      <td class="py-1 pr-4 font-medium whitespace-nowrap" :class="{ 'text-slate-400': !sc.enabled }">
                        {{ scheduleActionLabels[sc.action] }}
                      </td>
                      <td class="py-1 pr-4 whitespace-nowrap text-slate-600" :class="{ 'text-slate-400': !sc.enabled }">
                        {{ scheduleWhen(sc) }}
                      </td>
                      <td class="py-1 pr-4 whitespace-nowrap text-slate-500">
                        <template v-if="!sc.enabled">paused</template>
                        <template v-else-if="sc.nextRunAt" >
                          <span :title="fmtTime(sc.nextRunAt)">{{ relTime(sc.nextRunAt) }}</span>
                        </template>
                        <template v-else>—</template>
                      </td>
                      <td
                        class="py-1 pr-4 font-mono"
                        :class="sc.lastResult && sc.lastResult !== 'fired' ? 'text-amber-600' : 'text-slate-400'"
                        :title="sc.lastFiredAt ? `last attempt ${fmtTime(sc.lastFiredAt)}` : ''"
                      >
                        {{ sc.lastResult || 'never fired' }}
                      </td>
                      <td class="py-1 whitespace-nowrap text-right">
                        <button
                          class="mr-3 text-slate-500 hover:text-slate-900"
                          @click="openScheduleEdit(d, sc)"
                        >
                          Edit
                        </button>
                        <button
                          class="text-slate-500 hover:text-slate-900"
                          @click="toggleSchedule(d, sc)"
                        >
                          {{ sc.enabled ? 'Pause' : 'Resume' }}
                        </button>
                        <button
                          class="ml-3 text-red-400 hover:text-red-600"
                          @click="removeSchedule(d, sc)"
                        >
                          Delete
                        </button>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </td>
            </tr>
            <tr v-if="expandedRuns[d.id]" class="border-t border-slate-50 bg-slate-50">
              <td colspan="6" class="px-6 py-3">
                <p v-if="expandedRuns[d.id]!.length === 0" class="text-xs text-slate-400">
                  No runs yet.
                </p>
                <table v-else class="w-full text-xs">
                  <tbody>
                    <tr v-for="run in expandedRuns[d.id]" :key="run.id" class="align-top">
                      <td class="py-1 pr-4 font-medium whitespace-nowrap">
                        {{ run.type
                        }}<span v-if="run.triggeredBy" title="Started by a schedule" class="ml-1">⏱</span>
                      </td>
                      <td class="py-1 pr-4 whitespace-nowrap" :class="runStatusClass[run.status]">
                        {{ run.status }}
                      </td>
                      <td class="py-1 pr-4 whitespace-nowrap text-slate-400">
                        {{ fmtTime(run.startedAt ?? run.createdAt) }}
                      </td>
                      <td class="py-1 pr-4 whitespace-nowrap text-slate-400">{{ runDuration(run) }}</td>
                      <td class="py-1 pr-4">
                        <Sparkline
                          v-if="runSamples[run.id] && runSamples[run.id].length > 1"
                          :values="runSamples[run.id]"
                        />
                      </td>
                      <td
                        class="py-1 font-mono"
                        :class="runError(run) ? 'text-red-600' : 'text-slate-500'"
                      >
                        {{ runSummary(run) || '—' }}
                      </td>
                    </tr>
                  </tbody>
                </table>
              </td>
            </tr>
            <tr v-if="expandedApi[d.id]" class="border-t border-slate-50 bg-slate-50">
              <td colspan="6" class="px-6 py-3">
                <p class="mb-2 text-xs text-slate-500">
                  REST endpoints for this deployment — copy into a script (curl, PowerShell, …). Auth
                  only if the controller has a password/API key set.
                </p>
                <table class="w-full text-xs">
                  <tbody>
                    <tr v-for="e in apiEndpoints(d)" :key="e.action + e.method" class="align-top">
                      <td class="py-1 pr-3 whitespace-nowrap text-slate-600">{{ e.action }}</td>
                      <td class="py-1 pr-2 font-mono font-medium whitespace-nowrap">{{ e.method }}</td>
                      <td class="py-1 pr-2 font-mono break-all text-slate-700">{{ e.url }}</td>
                      <td class="py-1 pr-2 font-mono break-all text-slate-500">{{ e.body || '' }}</td>
                      <td class="py-1 whitespace-nowrap">
                        <button
                          class="text-sky-600 hover:text-sky-800"
                          @click="copyText(e.url, e.action + ':url')"
                        >
                          {{ copied === e.action + ':url' ? 'copied' : 'copy URL' }}
                        </button>
                        <button
                          v-if="e.body"
                          class="ml-2 text-sky-600 hover:text-sky-800"
                          @click="copyText(e.body!, e.action + ':body')"
                        >
                          {{ copied === e.action + ':body' ? 'copied' : 'copy body' }}
                        </button>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
  </div>
</template>
