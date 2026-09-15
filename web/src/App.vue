<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, UNAUTHORIZED_EVENT } from './api'
import Logo from './components/Logo.vue'

type AppState = 'loading' | 'login' | 'ready'

const state = ref<AppState>('loading')
const version = ref('')
const passwordRequired = ref(false)
const password = ref('')
const loginError = ref('')

const nav = [
  { to: '/', label: 'Dashboard' },
  { to: '/profiles', label: 'Profiles' },
  { to: '/connections', label: 'Connections' },
  { to: '/deployments', label: 'Deployments' },
]

onMounted(async () => {
  window.addEventListener(UNAUTHORIZED_EVENT, () => (state.value = 'login'))
  try {
    const [v, s] = await Promise.all([api.version(), api.session()])
    version.value = v.version
    passwordRequired.value = s.passwordRequired
    state.value = s.authenticated ? 'ready' : 'login'
  } catch {
    version.value = 'controller unreachable'
    state.value = 'ready'
  }
})

async function login() {
  loginError.value = ''
  try {
    await api.login(password.value)
    password.value = ''
    state.value = 'ready'
  } catch {
    loginError.value = 'Wrong password'
  }
}

async function logout() {
  await api.logout()
  state.value = 'login'
}
</script>

<template>
  <div class="min-h-screen bg-slate-50 text-slate-900">
    <!-- Login gate (UI-1: only when a password is configured) -->
    <div v-if="state === 'login'" class="flex min-h-screen items-center justify-center">
      <form class="w-80 rounded-xl border border-slate-200 bg-white p-8 shadow-sm" @submit.prevent="login">
        <div class="mb-4 flex items-center gap-2.5">
          <Logo :size="32" />
          <h1 class="text-lg font-semibold">GhostFleet</h1>
        </div>
        <p class="mb-6 text-sm text-slate-500">This controller is password protected.</p>
        <input
          v-model="password"
          type="password"
          placeholder="Password"
          autofocus
          class="mb-3 w-full rounded-lg border border-slate-300 px-3 py-2 text-sm focus:border-slate-500 focus:outline-none"
        />
        <p v-if="loginError" class="mb-3 text-sm text-red-600">{{ loginError }}</p>
        <button
          type="submit"
          class="w-full rounded-lg bg-slate-900 px-3 py-2 text-sm font-medium text-white hover:bg-slate-700"
        >
          Sign in
        </button>
      </form>
    </div>

    <div v-else-if="state === 'ready'" class="flex min-h-screen">
      <aside class="flex w-56 flex-col border-r border-slate-200 bg-white">
        <div class="flex items-center gap-2.5 px-5 py-4">
          <Logo :size="28" />
          <div class="leading-tight">
            <h1 class="font-semibold tracking-tight">GhostFleet</h1>
            <p class="text-xs text-slate-400">Synthetic data generator</p>
          </div>
        </div>
        <nav class="flex-1 space-y-1 px-3">
          <RouterLink
            v-for="item in nav"
            :key="item.to"
            :to="item.to"
            class="block rounded-lg px-3 py-2 text-sm text-slate-600 hover:bg-slate-100"
            exact-active-class="bg-slate-100 font-medium text-slate-900"
          >
            {{ item.label }}
          </RouterLink>
        </nav>
        <div class="border-t border-slate-100 px-5 py-3 text-xs text-slate-400">
          <button v-if="passwordRequired" class="mb-1 block hover:text-slate-600" @click="logout">
            Sign out
          </button>
          <span class="font-mono">{{ version }}</span>
        </div>
      </aside>

      <main class="flex-1 overflow-y-auto px-8 py-8">
        <RouterView />
      </main>
    </div>
  </div>
</template>
