<script setup lang="ts">
import type { ProfileSpec } from '../api'

// The spec object is edited in place; parents own its lifecycle.
defineProps<{ spec: ProfileSpec }>()

const fieldClass =
  'w-full rounded-lg border border-slate-300 px-3 py-1.5 text-sm focus:border-slate-500 focus:outline-none'
const labelClass = 'block text-xs font-medium text-slate-500 mb-1'
</script>

<template>
  <div>
    <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <div>
        <label :class="labelClass">Tag (on all VMs)</label>
        <input v-model="spec.tag" :class="fieldClass" placeholder='e.g. "ghostfleet" or "backup=ghostfleet"' />
      </div>
      <div>
        <label :class="labelClass">After fill</label>
        <select v-model="spec.afterFill" :class="fieldClass">
          <option value="shutdown">Shut down VMs</option>
          <option value="keep-running">Keep running</option>
        </select>
      </div>
    </div>

    <h4 class="mt-6 mb-2 text-xs font-semibold uppercase tracking-wide text-slate-400">Fleet</h4>
    <div class="grid grid-cols-2 gap-4 lg:grid-cols-5">
      <div>
        <label :class="labelClass">VM count</label>
        <input v-model.number="spec.vmCount" type="number" min="1" max="1000" :class="fieldClass" />
      </div>
      <div>
        <label :class="labelClass">Name prefix</label>
        <input v-model="spec.namePrefix" :class="fieldClass" pattern="[a-z][a-z0-9-]*" />
      </div>
      <div>
        <label :class="labelClass">Counter digits</label>
        <input v-model.number="spec.namePad" type="number" min="1" max="8" :class="fieldClass" />
      </div>
      <div>
        <label :class="labelClass">vCPUs</label>
        <input v-model.number="spec.vcpus" type="number" min="1" max="64" :class="fieldClass" />
      </div>
      <div>
        <label :class="labelClass">Memory (MiB)</label>
        <input v-model.number="spec.memoryMiB" type="number" min="512" step="512" :class="fieldClass" />
      </div>
    </div>

    <h4 class="mt-6 mb-2 text-xs font-semibold uppercase tracking-wide text-slate-400">Disks</h4>
    <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <div>
        <label :class="labelClass">Disks per VM</label>
        <input v-model.number="spec.disksPerVM" type="number" min="1" max="60" :class="fieldClass" />
      </div>
      <div>
        <label :class="labelClass">Disk size (GiB)</label>
        <input v-model.number="spec.diskSizeGiB" type="number" min="1" :class="fieldClass" />
      </div>
      <div>
        <label :class="labelClass">Data per disk (GiB)</label>
        <input v-model.number="spec.dataPerDiskGiB" type="number" min="0" :class="fieldClass" />
      </div>
      <div class="flex items-end pb-1.5">
        <label class="flex items-center gap-2 text-sm text-slate-600">
          <input v-model="spec.thick" type="checkbox" class="rounded" />
          Thick provisioned
        </label>
      </div>
    </div>

    <h4 class="mt-6 mb-2 text-xs font-semibold uppercase tracking-wide text-slate-400">
      Data characteristics (%)
    </h4>
    <div class="grid grid-cols-2 gap-4 lg:grid-cols-5">
      <div>
        <label :class="labelClass">Compressible</label>
        <input v-model.number="spec.compressPercent" type="number" step="any" min="0" max="100" :class="fieldClass" />
      </div>
      <div>
        <label :class="labelClass">Dedup within VM</label>
        <input v-model.number="spec.dedupePercent" type="number" step="any" min="0" max="100" :class="fieldClass" />
      </div>
      <div>
        <label :class="labelClass">Dedup across VMs</label>
        <input v-model.number="spec.crossVMDedupePercent" type="number" step="any" min="0" max="100" :class="fieldClass" />
      </div>
      <div>
        <label :class="labelClass">Change per run</label>
        <input v-model.number="spec.changePercent" type="number" step="any" min="0" max="100" :class="fieldClass" />
      </div>
      <div>
        <label :class="labelClass">Growth per run</label>
        <input v-model.number="spec.growthPercent" type="number" step="any" min="0" max="1000" :class="fieldClass" />
      </div>
    </div>

    <h4 class="mt-6 mb-2 text-xs font-semibold uppercase tracking-wide text-slate-400">Throttling</h4>
    <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <div>
        <label :class="labelClass">Rate cap (MB/s, 0 = off)</label>
        <input v-model.number="spec.rateLimitMBps" type="number" min="0" :class="fieldClass" />
      </div>
      <div>
        <label :class="labelClass">Max parallel VMs (0 = all)</label>
        <input v-model.number="spec.maxParallelVMs" type="number" min="0" :class="fieldClass" />
      </div>
    </div>
  </div>
</template>
