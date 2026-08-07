import { createApp } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import App from './App.vue'
import './styles/index.css'
import SendView from './views/SendView.vue'
import ReceiveView from './views/ReceiveView.vue'
import HistoryView from './views/HistoryView.vue'
import SettingsView from './views/SettingsView.vue'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', redirect: '/send' },
    { path: '/send', component: SendView, meta: { title: 'Send' } },
    { path: '/receive', component: ReceiveView, meta: { title: 'Receive' } },
    { path: '/history', component: HistoryView, meta: { title: 'History' } },
    { path: '/settings', component: SettingsView, meta: { title: 'Settings' } },
  ],
})

createApp(App).use(router).mount('#app')
