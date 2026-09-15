<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type Profile, type ProfileSpec } from '../api'
import ProfileEditor from '../components/ProfileEditor.vue'

const profiles = ref<Profile[]>([])
const editing = ref<Profile | null>(null)
const creating = ref(false)
const error = ref('')

async function reload() {
  profiles.value = await api.listProfiles()
}

onMounted(() => reload().catch((e) => (error.value = String(e))))

async function save(name: string, spec: ProfileSpec) {
  error.value = ''
  try {
    if (editing.value) {
      await api.updateProfile(editing.value.id, {
        name: name !== editing.value.name ? name : undefined,
        spec,
      })
    } else {
      await api.createProfile(name, spec)
    }
    creating.value = false
    editing.value = null
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

async function remove(p: Profile) {
  if (!confirm(`Delete profile "${p.name}" and all its versions?`)) return
  error.value = ''
  try {
    await api.deleteProfile(p.id)
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

// exportProfile downloads the profile's current version as a JSON document.
async function exportProfile(p: Profile) {
  error.value = ''
  try {
    const res = await fetch(api.exportProfileURL(p.id))
    if (!res.ok) throw new Error(`export failed: ${res.status}`)
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${p.name}.ghostfleet-profile.json`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  } catch (e) {
    error.value = String(e)
  }
}

// importProfile reads a previously exported JSON document and creates a new
// profile from it, letting the user adjust the name first (names are unique).
const fileInput = ref<HTMLInputElement | null>(null)
async function onImportFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = '' // allow re-importing the same file later
  if (!file) return
  error.value = ''
  try {
    const doc = JSON.parse(await file.text()) as { kind?: string; name?: string; spec?: ProfileSpec }
    if (!doc.spec) throw new Error('not a GhostFleet profile document (no spec)')
    const name = prompt('Name for the imported profile:', doc.name ?? '')
    if (name === null) return // cancelled
    await api.importProfile({ kind: doc.kind, name, spec: doc.spec })
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}
</script>

<template>
  <div>
    <div class="mb-6 flex items-center justify-between">
      <h2 class="text-xl font-semibold">Profiles</h2>
      <div v-if="!creating && !editing" class="flex items-center gap-2">
        <input
          ref="fileInput"
          type="file"
          accept="application/json,.json"
          class="hidden"
          @change="onImportFile"
        />
        <button
          class="rounded-lg border border-slate-300 px-4 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
          @click="fileInput?.click()"
        >
          Import
        </button>
        <button
          class="rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
          @click="creating = true"
        >
          New profile
        </button>
      </div>
    </div>

    <p v-if="error" class="mb-4 text-sm text-red-600">{{ error }}</p>

    <ProfileEditor
      v-if="creating || editing"
      :key="editing?.id ?? 'new'"
      :profile="editing ?? undefined"
      class="mb-6"
      @save="save"
      @cancel="((creating = false), (editing = null))"
    />

    <div class="overflow-hidden rounded-xl border border-slate-200 bg-white">
      <table class="w-full text-sm">
        <thead class="bg-slate-50 text-left text-xs uppercase tracking-wide text-slate-400">
          <tr>
            <th class="px-4 py-3">Name</th>
            <th class="px-4 py-3">Version</th>
            <th class="px-4 py-3">VMs</th>
            <th class="px-4 py-3">Disks/VM</th>
            <th class="px-4 py-3">Data total</th>
            <th class="px-4 py-3">Tag</th>
            <th class="px-4 py-3"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="profiles.length === 0">
            <td colspan="7" class="px-4 py-8 text-center text-slate-400">
              No profiles yet — create one to define your source data.
            </td>
          </tr>
          <tr v-for="p in profiles" :key="p.id" class="border-t border-slate-100">
            <td class="px-4 py-3 font-medium">{{ p.name }}</td>
            <td class="px-4 py-3">v{{ p.currentVersion }}</td>
            <td class="px-4 py-3">{{ p.spec.vmCount }} × {{ p.spec.namePrefix }}-…</td>
            <td class="px-4 py-3">{{ p.spec.disksPerVM }} × {{ p.spec.diskSizeGiB }} GiB</td>
            <td class="px-4 py-3">
              {{ p.spec.vmCount * p.spec.disksPerVM * p.spec.dataPerDiskGiB }} GiB
            </td>
            <td class="px-4 py-3 text-slate-500">{{ p.spec.tag || '—' }}</td>
            <td class="px-4 py-3 text-right whitespace-nowrap">
              <button class="text-slate-500 hover:text-slate-900" @click="editing = p">Edit</button>
              <button class="ml-3 text-slate-500 hover:text-slate-900" @click="exportProfile(p)">
                Export
              </button>
              <button class="ml-3 text-red-400 hover:text-red-600" @click="remove(p)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
