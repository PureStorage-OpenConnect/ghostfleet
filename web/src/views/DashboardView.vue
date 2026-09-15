<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { api, type Deployment, type Profile, type Connection, type Run } from '../api'
import Logo from '../components/Logo.vue'

const profiles = ref<Profile[]>([])
const connections = ref<Connection[]>([])
const deployments = ref<Deployment[]>([])
const runs = ref<(Run & { deploymentName: string })[]>([])
const error = ref('')

const active = computed(() => deployments.value.filter((d) => d.status !== 'deleted'))
const totalVMs = computed(() => active.value.reduce((n, d) => n + d.spec.vmCount, 0))

// Bytes actually generated across all succeeded fill/incremental runs.
const totalGeneratedGiB = computed(() => {
  let bytes = 0
  for (const r of runs.value) {
    if (r.status !== 'succeeded') continue
    if (r.type !== 'initial-fill' && r.type !== 'incremental') continue
    try {
      bytes += (JSON.parse(r.stats ?? '{}') as { bytesWritten?: number }).bytesWritten ?? 0
    } catch {
      /* ignore */
    }
  }
  return bytes / 1024 ** 3
})

const recentRuns = computed(() =>
  [...runs.value]
    .sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1))
    .slice(0, 10),
)

function runStat(r: Run, key: string): string {
  try {
    const v = (JSON.parse(r.stats ?? '{}') as Record<string, number>)[key]
    return v === undefined ? '—' : String(v)
  } catch {
    return '—'
  }
}

const statusClass: Record<string, string> = {
  succeeded: 'text-emerald-600',
  failed: 'text-red-600',
  running: 'text-amber-600',
  pending: 'text-slate-400',
}

onMounted(async () => {
  try {
    ;[profiles.value, connections.value, deployments.value] = await Promise.all([
      api.listProfiles(),
      api.listConnections(),
      api.listDeployments(),
    ])
    const all = await Promise.all(
      deployments.value.map((d) =>
        api
          .listRuns(d.id)
          .then((rs) => rs.map((r) => ({ ...r, deploymentName: d.name })))
          .catch(() => []),
      ),
    )
    runs.value = all.flat()
  } catch (e) {
    error.value = String(e)
  }
})
</script>

<template>
  <div>
    <div class="mb-6 flex items-center gap-4">
      <Logo :size="48" />
      <div>
        <h2 class="text-xl font-semibold leading-tight">GhostFleet</h2>
        <p class="text-sm text-slate-500">
          Calibrated synthetic data generation for storage — load, dedup/compression validation,
          capacity planning and backup testing.
        </p>
      </div>
    </div>
    <p v-if="error" class="mb-4 text-sm text-red-600">{{ error }}</p>

    <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <div class="rounded-xl border border-slate-200 bg-white p-5">
        <p class="text-sm text-slate-500">Active deployments</p>
        <p class="mt-1 text-3xl font-semibold">{{ active.length }}</p>
      </div>
      <div class="rounded-xl border border-slate-200 bg-white p-5">
        <p class="text-sm text-slate-500">Configured VMs</p>
        <p class="mt-1 text-3xl font-semibold">{{ totalVMs }}</p>
      </div>
      <div class="rounded-xl border border-slate-200 bg-white p-5">
        <p class="text-sm text-slate-500">Data generated</p>
        <p class="mt-1 text-3xl font-semibold">
          {{ totalGeneratedGiB.toFixed(0) }}
          <span class="text-base font-normal text-slate-400">GiB</span>
        </p>
      </div>
      <div class="rounded-xl border border-slate-200 bg-white p-5">
        <p class="text-sm text-slate-500">Profiles / connections</p>
        <p class="mt-1 text-3xl font-semibold">
          {{ profiles.length }}<span class="text-base font-normal text-slate-400"> / {{ connections.length }}</span>
        </p>
      </div>
    </div>

    <h3 class="mt-8 mb-3 text-sm font-semibold text-slate-600">Recent runs</h3>
    <div class="overflow-hidden rounded-xl border border-slate-200 bg-white">
      <table class="w-full text-sm">
        <thead class="bg-slate-50 text-left text-xs uppercase tracking-wide text-slate-400">
          <tr>
            <th class="px-4 py-3">Deployment</th>
            <th class="px-4 py-3">Run</th>
            <th class="px-4 py-3">Status</th>
            <th class="px-4 py-3">Data</th>
            <th class="px-4 py-3">Avg MB/s</th>
            <th class="px-4 py-3">When</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="recentRuns.length === 0">
            <td colspan="6" class="px-4 py-8 text-center text-slate-400">No runs yet.</td>
          </tr>
          <tr v-for="r in recentRuns" :key="r.id" class="border-t border-slate-100">
            <td class="px-4 py-2 font-medium">{{ r.deploymentName }}</td>
            <td class="px-4 py-2">{{ r.type }}</td>
            <td class="px-4 py-2" :class="statusClass[r.status]">{{ r.status }}</td>
            <td class="px-4 py-2 text-slate-500">
              <template v-if="runStat(r, 'bytesWritten') !== '—'">
                {{ (Number(runStat(r, 'bytesWritten')) / 1024 ** 3).toFixed(1) }} GiB
              </template>
              <template v-else>—</template>
            </td>
            <td class="px-4 py-2 text-slate-500">{{ runStat(r, 'avgMBps') }}</td>
            <td class="px-4 py-2 text-slate-400">{{ new Date(r.createdAt).toLocaleString() }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
