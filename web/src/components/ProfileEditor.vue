<script setup lang="ts">
import { reactive, ref } from 'vue'
import type { Profile, ProfileSpec } from '../api'
import SpecForm from './SpecForm.vue'

const props = defineProps<{ profile?: Profile }>()
const emit = defineEmits<{
  save: [name: string, spec: ProfileSpec]
  cancel: []
}>()

const name = ref(props.profile?.name ?? '')
const spec = reactive<ProfileSpec>(props.profile ? { ...props.profile.spec } : defaultSpec())

function defaultSpec(): ProfileSpec {
  return {
    vmCount: 4,
    namePrefix: 'ghost',
    namePad: 4,
    vcpus: 2,
    memoryMiB: 2048,
    disksPerVM: 1,
    diskSizeGiB: 100,
    dataPerDiskGiB: 50,
    thick: false,
    compressPercent: 50,
    dedupePercent: 10,
    crossVMDedupePercent: 10,
    changePercent: 5,
    growthPercent: 2,
    tag: '',
    afterFill: 'shutdown',
    rateLimitMBps: 0,
    maxParallelVMs: 0,
  }
}
</script>

<template>
  <form
    class="rounded-xl border border-slate-200 bg-white p-6"
    @submit.prevent="emit('save', name, spec)"
  >
    <h3 class="mb-4 font-medium">
      {{ props.profile ? `Edit profile (creates version ${props.profile.currentVersion + 1})` : 'New profile' }}
    </h3>

    <div class="mb-4 max-w-md">
      <label class="mb-1 block text-xs font-medium text-slate-500">Profile name</label>
      <input
        v-model="name"
        class="w-full rounded-lg border border-slate-300 px-3 py-1.5 text-sm focus:border-slate-500 focus:outline-none"
        required
        placeholder="e.g. lab-baseline"
      />
    </div>

    <SpecForm :spec="spec" />

    <div class="mt-6 flex gap-3">
      <button
        type="submit"
        class="rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
      >
        {{ props.profile ? 'Save as new version' : 'Create profile' }}
      </button>
      <button
        type="button"
        class="rounded-lg border border-slate-300 px-4 py-2 text-sm hover:bg-slate-100"
        @click="emit('cancel')"
      >
        Cancel
      </button>
    </div>
  </form>
</template>
