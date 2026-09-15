import { createRouter, createWebHistory } from 'vue-router'
import DashboardView from './views/DashboardView.vue'
import ProfilesView from './views/ProfilesView.vue'
import ConnectionsView from './views/ConnectionsView.vue'
import DeploymentsView from './views/DeploymentsView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'dashboard', component: DashboardView },
    { path: '/profiles', name: 'profiles', component: ProfilesView },
    { path: '/connections', name: 'connections', component: ConnectionsView },
    { path: '/deployments', name: 'deployments', component: DeploymentsView },
  ],
})
