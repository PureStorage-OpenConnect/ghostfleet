// Typed client for the GhostFleet controller API.

export interface ProfileSpec {
  vmCount: number
  namePrefix: string
  namePad?: number
  vcpus?: number
  memoryMiB?: number
  disksPerVM: number
  diskSizeGiB: number
  dataPerDiskGiB: number
  thick?: boolean
  compressPercent: number
  dedupePercent: number
  crossVMDedupePercent: number
  changePercent: number
  growthPercent: number
  tag?: string
  afterFill?: 'shutdown' | 'keep-running'
  rateLimitMBps?: number
  maxParallelVMs?: number
}

export interface Profile {
  id: string
  name: string
  currentVersion: number
  spec: ProfileSpec
  createdAt: string
  updatedAt: string
}

export interface Connection {
  id: string
  name: string
  plugin: string
  endpoint: string
  username: string
  insecureTLS: boolean
  createdAt: string
  updatedAt: string
}

export interface Deployment {
  id: string
  name: string
  profileId?: string
  profileVersion?: number
  connectionId: string
  spec: ProfileSpec
  placement: Record<string, string>
  status: string
  origin?: string // 'adopted' = built from discovered VMs (never reconciled)
  createdAt: string
  updatedAt: string
  filled?: boolean // has a succeeded initial-fill run (gates incremental)
  running?: boolean // a job is currently running (enables cancel)
}

export interface VMConflict {
  name: string
  ref: string
  ownerId?: string
  ownerName?: string
  ownerKnown: boolean
  ownerSince?: string
}

export interface SessionStatus {
  passwordRequired: boolean
  authenticated: boolean
}

export interface HypervisorInfo {
  product: string
  version: string
}

export interface DatastoreOption {
  name: string
  freeBytes: number
  capacityBytes: number
}

export interface HostOption {
  name: string
  cluster: string
}

export interface PlacementOptions {
  datacenters?: string[]
  clusters: string[]
  hosts?: HostOption[]
  resourcePools?: string[]
  datastores: DatastoreOption[]
  networks: string[]
  folders?: string[]
}

export interface ManagedVM {
  id: string
  deploymentId: string
  name: string
  ref: string
  diskCount: number
  diskSizeGiB: number
  mac?: string
  agentStatus: string
  agentSeenAt?: string
  dataState?: string // '' (unknown) | empty | partial | filled — what the disks hold
  dataRunId?: string
  dataManifest?: boolean
  dataSeenAt?: string
  fillRunId?: string
  fillStatus?: string
  bytesWritten: number
  bytesTotal: number
  mbps: number
  fillError?: string
  createdAt: string
}

export interface Run {
  id: string
  deploymentId: string
  type: string
  status: string
  startedAt?: string
  finishedAt?: string
  stats?: string
  triggeredBy?: string // schedule ID when the run was fired by a timer
  createdAt: string
}

// Schedule fires one run type against a deployment on a timer.
export interface Schedule {
  id: string
  deploymentId: string
  action: 'incremental' | 'verify' | 'power-on' | 'power-off' | 'teardown'
  kind: 'every' | 'daily' | 'once'
  spec: string // every: Go duration; daily: "HH:MM[@Mon,Fri]"; once: RFC3339
  timezone?: string
  enabled: boolean
  nextRunAt?: string
  lastFiredAt?: string
  lastRunId?: string
  lastResult?: string
  createdAt: string
  updatedAt: string
}

export interface ScheduleRequest {
  action: Schedule['action']
  kind: Schedule['kind']
  spec: string
  timezone?: string
  enabled?: boolean
}

// IdentityMarker is the on-disk identity a fill stamps onto each data disk;
// discovery reads it back to tell where a restored VM came from.
export interface IdentityMarker {
  deploymentId: string
  deploymentName: string
  vmName: string
  diskIndex: number
  spec: ProfileSpec
}

export interface DiscoveryDisk {
  device: string
  sizeGiB: number
  filesystem?: string
  identity?: IdentityMarker
  runId?: string
  runBytes?: number
  manifest: boolean
  fileCount?: number
}

// DiscoveredVM is an unknown machine that PXE-booted on the isolated network
// (typically a backup restore of a GhostFleet VM, which comes up with a fresh
// MAC), inspected by a report-only discovery agent and adoptable.
export interface DiscoveredVM {
  id: string
  mac: string
  status: 'new' | 'adopted'
  inspection?: string // JSON DiscoveryReport ({disks: DiscoveryDisk[]})
  firstSeenAt: string
  lastSeenAt: string
  agentSeenAt?: string
}

export interface AdoptResult {
  deployment: Deployment
  vm: ManagedVM
  warning?: string
}

export interface RunSample {
  ts: string
  mbps: number
  bytesWritten: number
}

export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

/** Fired when any API call returns 401, so the app can show the login screen. */
export const UNAUTHORIZED_EVENT = 'ghostfleet:unauthorized'

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (res.status === 401 && path !== '/api/v1/session') {
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT))
  }
  if (!res.ok) {
    let message = res.statusText
    try {
      message = ((await res.json()) as { error?: string }).error ?? message
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(res.status, message)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const api = {
  version: () => request<{ version: string }>('GET', '/api/v1/version'),

  session: () => request<SessionStatus>('GET', '/api/v1/session'),
  login: (password: string) => request<void>('POST', '/api/v1/session', { password }),
  logout: () => request<void>('DELETE', '/api/v1/session'),

  listProfiles: () => request<Profile[]>('GET', '/api/v1/profiles'),
  createProfile: (name: string, spec: ProfileSpec) =>
    request<Profile>('POST', '/api/v1/profiles', { name, spec }),
  updateProfile: (id: string, body: { name?: string; spec?: ProfileSpec }) =>
    request<Profile>('PUT', `/api/v1/profiles/${id}`, body),
  deleteProfile: (id: string) => request<void>('DELETE', `/api/v1/profiles/${id}`),
  exportProfileURL: (id: string) => `/api/v1/profiles/${id}/export`,
  importProfile: (doc: { kind?: string; name: string; spec: ProfileSpec }) =>
    request<Profile>('POST', '/api/v1/profiles/import', doc),

  listConnections: () => request<Connection[]>('GET', '/api/v1/connections'),
  createConnection: (body: Partial<Connection> & { secret: string }) =>
    request<Connection>('POST', '/api/v1/connections', body),
  updateConnection: (id: string, body: Partial<Connection> & { secret?: string }) =>
    request<Connection>('PUT', `/api/v1/connections/${id}`, body),
  deleteConnection: (id: string) => request<void>('DELETE', `/api/v1/connections/${id}`),

  validateConnection: (id: string) =>
    request<HypervisorInfo>('POST', `/api/v1/connections/${id}/validate`),
  listPlacement: (id: string) =>
    request<PlacementOptions>('GET', `/api/v1/connections/${id}/placement`),

  listDeployments: () => request<Deployment[]>('GET', '/api/v1/deployments'),
  createDeployment: (body: {
    name: string
    profileId: string
    profileVersion?: number
    connectionId: string
    spec?: ProfileSpec
    placement: Record<string, string>
  }) => request<Deployment>('POST', '/api/v1/deployments', body),
  deployDeployment: (id: string, onConflict?: 'abort' | 'adopt' | 'clean') =>
    request<Run>('POST', `/api/v1/deployments/${id}/deploy`, onConflict ? { onConflict } : undefined),
  deploymentConflicts: (id: string) =>
    request<VMConflict[]>('GET', `/api/v1/deployments/${id}/conflicts`),
  powerDeployment: (id: string, on: boolean) =>
    request<Run>('POST', `/api/v1/deployments/${id}/power`, { on }),
  fillDeployment: (id: string, type: 'initial-fill' | 'incremental' | 'verify') =>
    request<Run>('POST', `/api/v1/deployments/${id}/fill`, { type }),
  cancelDeployment: (id: string) => request<void>('POST', `/api/v1/deployments/${id}/cancel`),
  listRunSamples: (deploymentId: string, runId: string) =>
    request<RunSample[]>('GET', `/api/v1/deployments/${deploymentId}/runs/${runId}/samples`),
  updateDeploymentSpec: (id: string, spec: ProfileSpec) =>
    request<Deployment>('PUT', `/api/v1/deployments/${id}/spec`, { spec }),
  deleteDeployment: (id: string) => request<Run | void>('DELETE', `/api/v1/deployments/${id}`),
  listDeploymentVMs: (id: string) => request<ManagedVM[]>('GET', `/api/v1/deployments/${id}/vms`),
  getVMConsole: (deploymentId: string, vmId: string, session = 0) =>
    request<{
      sessionCount: number
      session: number
      sessions: { n: number; startedAt: string; partial: boolean }[]
      console: string
    }>(
      'GET',
      `/api/v1/deployments/${deploymentId}/vms/${vmId}/console${session ? `?session=${session}` : ''}`,
    ),
  listRuns: (id: string) => request<Run[]>('GET', `/api/v1/deployments/${id}/runs`),

  listSchedules: (deploymentId: string) =>
    request<Schedule[]>('GET', `/api/v1/deployments/${deploymentId}/schedules`),
  createSchedule: (deploymentId: string, body: ScheduleRequest) =>
    request<Schedule>('POST', `/api/v1/deployments/${deploymentId}/schedules`, body),
  updateSchedule: (id: string, body: ScheduleRequest) =>
    request<Schedule>('PUT', `/api/v1/schedules/${id}`, body),
  deleteSchedule: (id: string) => request<void>('DELETE', `/api/v1/schedules/${id}`),

  listDiscoveredVMs: () => request<DiscoveredVM[]>('GET', '/api/v1/discovered-vms'),
  deleteDiscoveredVM: (id: string) => request<void>('DELETE', `/api/v1/discovered-vms/${id}`),
  adoptDiscoveredVM: (
    id: string,
    body: { mode: 'rebind' | 'new-deployment'; name?: string; connectionId?: string; deploymentId?: string },
  ) => request<AdoptResult>('POST', `/api/v1/discovered-vms/${id}/adopt`, body),
}
