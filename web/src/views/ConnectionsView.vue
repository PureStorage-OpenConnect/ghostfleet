<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { api, type Connection } from '../api'

const connections = ref<Connection[]>([])
const showForm = ref(false)
const editingId = ref<string | null>(null)
const error = ref('')
const testResult = ref<Record<string, string>>({})

async function testConnection(c: Connection) {
  testResult.value = { ...testResult.value, [c.id]: '…' }
  try {
    const info = await api.validateConnection(c.id)
    testResult.value = { ...testResult.value, [c.id]: `✓ ${info.product} ${info.version}` }
  } catch (e) {
    testResult.value = { ...testResult.value, [c.id]: `✗ ${e}` }
  }
}

const form = reactive({
  name: '',
  plugin: 'vsphere',
  endpoint: '',
  username: '',
  secret: '',
  insecureTLS: false,
})

function openCreate() {
  Object.assign(form, {
    name: '',
    plugin: 'vsphere',
    endpoint: '',
    username: '',
    secret: '',
    insecureTLS: false,
  })
  editingId.value = null
  showForm.value = true
}

function openEdit(c: Connection) {
  Object.assign(form, {
    name: c.name,
    plugin: c.plugin,
    endpoint: c.endpoint,
    username: c.username,
    secret: '', // empty keeps the stored secret
    insecureTLS: c.insecureTLS,
  })
  editingId.value = c.id
  showForm.value = true
}

async function reload() {
  connections.value = await api.listConnections()
}

onMounted(() => reload().catch((e) => (error.value = String(e))))

async function save() {
  error.value = ''
  try {
    if (editingId.value) {
      await api.updateConnection(editingId.value, { ...form })
    } else {
      await api.createConnection({ ...form })
    }
    showForm.value = false
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

async function remove(c: Connection) {
  if (!confirm(`Delete connection "${c.name}"?`)) return
  error.value = ''
  try {
    await api.deleteConnection(c.id)
    await reload()
  } catch (e) {
    error.value = String(e)
  }
}

const fieldClass =
  'w-full rounded-lg border border-slate-300 px-3 py-1.5 text-sm focus:border-slate-500 focus:outline-none'
const labelClass = 'block text-xs font-medium text-slate-500 mb-1'
</script>

<template>
  <div>
    <div class="mb-6 flex items-center justify-between">
      <h2 class="text-xl font-semibold">Hypervisor connections</h2>
      <button
        v-if="!showForm"
        class="rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
        @click="openCreate"
      >
        New connection
      </button>
    </div>

    <p v-if="error" class="mb-4 text-sm text-red-600">{{ error }}</p>

    <form v-if="showForm" class="mb-6 rounded-xl border border-slate-200 bg-white p-6" @submit.prevent="save">
      <h3 class="mb-4 font-medium">{{ editingId ? 'Edit connection' : 'New connection' }}</h3>
      <div class="grid grid-cols-2 gap-4 lg:grid-cols-3">
        <div>
          <label :class="labelClass">Name</label>
          <input v-model="form.name" :class="fieldClass" required placeholder="e.g. lab-vcenter" />
        </div>
        <div>
          <label :class="labelClass">Platform</label>
          <select v-model="form.plugin" :class="fieldClass">
            <option value="vsphere">VMware vSphere (vCenter)</option>
          </select>
        </div>
        <div>
          <label :class="labelClass">Endpoint URL</label>
          <input v-model="form.endpoint" :class="fieldClass" required placeholder="https://vcenter.lab.example" />
        </div>
        <div>
          <label :class="labelClass">Username</label>
          <input v-model="form.username" :class="fieldClass" required placeholder="administrator@vsphere.local" />
        </div>
        <div>
          <label :class="labelClass">Password {{ editingId ? '(empty = keep current)' : '' }}</label>
          <input v-model="form.secret" type="password" :class="fieldClass" :required="!editingId" />
        </div>
        <div class="flex items-end pb-1.5">
          <label class="flex items-center gap-2 text-sm text-slate-600">
            <input v-model="form.insecureTLS" type="checkbox" class="rounded" />
            Skip TLS verification
          </label>
        </div>
      </div>
      <div class="mt-6 flex gap-3">
        <button
          type="submit"
          class="rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
        >
          {{ editingId ? 'Save' : 'Create connection' }}
        </button>
        <button
          type="button"
          class="rounded-lg border border-slate-300 px-4 py-2 text-sm hover:bg-slate-100"
          @click="showForm = false"
        >
          Cancel
        </button>
      </div>
    </form>

    <div class="overflow-hidden rounded-xl border border-slate-200 bg-white">
      <table class="w-full text-sm">
        <thead class="bg-slate-50 text-left text-xs uppercase tracking-wide text-slate-400">
          <tr>
            <th class="px-4 py-3">Name</th>
            <th class="px-4 py-3">Platform</th>
            <th class="px-4 py-3">Endpoint</th>
            <th class="px-4 py-3">Username</th>
            <th class="px-4 py-3"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="connections.length === 0">
            <td colspan="5" class="px-4 py-8 text-center text-slate-400">
              No connections yet — add your vCenter to deploy fleets.
            </td>
          </tr>
          <tr v-for="c in connections" :key="c.id" class="border-t border-slate-100">
            <td class="px-4 py-3 font-medium">{{ c.name }}</td>
            <td class="px-4 py-3">{{ c.plugin }}</td>
            <td class="px-4 py-3 text-slate-500">{{ c.endpoint }}</td>
            <td class="px-4 py-3 text-slate-500">{{ c.username }}</td>
            <td class="px-4 py-3 text-right whitespace-nowrap">
              <span v-if="testResult[c.id]" class="mr-3 text-xs text-slate-500">{{ testResult[c.id] }}</span>
              <button class="text-slate-500 hover:text-slate-900" @click="testConnection(c)">Test</button>
              <button class="ml-3 text-slate-500 hover:text-slate-900" @click="openEdit(c)">Edit</button>
              <button class="ml-3 text-red-400 hover:text-red-600" @click="remove(c)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
