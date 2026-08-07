<script setup lang="ts">
import { RouterLink } from 'vue-router'
import Icon from '../common/Icon.vue'
import { useDiscovery } from '../../composables/useDiscovery'
import { useUI } from '../../composables/useUI'

const { count } = useDiscovery()
const { showPair } = useUI()

const nav = [
  { to: '/send', label: 'Send', icon: 'send' },
  { to: '/receive', label: 'Receive', icon: 'receive' },
  { to: '/history', label: 'History', icon: 'history' },
  { to: '/settings', label: 'Settings', icon: 'settings' },
]
</script>

<template>
  <div class="flex h-full flex-col bg-ink-deep md:flex-row">
    <!-- Desktop sidebar -->
    <aside class="hidden w-72 shrink-0 flex-col border-r border-white/5 bg-surface-0 md:flex">
      <div class="drag-region border-b border-white/5 px-5 py-5"></div>

      <nav class="flex flex-col gap-1 px-3">
        <RouterLink
          v-for="n in nav"
          :key="n.to"
          :to="n.to"
          class="nav-item"
          active-class="nav-item--active"
        >
          <Icon :name="n.icon" />
          <span>{{ n.label }}</span>
        </RouterLink>
      </nav>

      <div class="mt-auto border-t border-white/5 p-3">
        <button class="btn-ghost w-full justify-start" @click="showPair = true">
          <Icon name="qrcode" />
          <span>Pair device</span>
        </button>
        <div class="mt-3 flex items-center gap-2 px-2 text-xs text-content-faint">
          <span class="h-2 w-2 rounded-full" :class="count ? 'bg-ok' : 'bg-content-faint'"></span>
          {{ count }} device{{ count === 1 ? '' : 's' }} nearby
        </div>
      </div>
    </aside>

    <!-- Mobile header -->
    <header class="drag-region flex items-center justify-end border-b border-white/5 bg-surface-0 px-4 py-3 md:hidden">
      <button class="btn-ghost" @click="showPair = true" aria-label="Pair device">
        <Icon name="qrcode" />
      </button>
    </header>

    <!-- Content -->
    <main class="flex-1 overflow-hidden">
      <slot />
    </main>

    <!-- Mobile bottom nav -->
    <nav class="flex border-t border-white/5 bg-surface-0 md:hidden">
      <RouterLink
        v-for="n in nav"
        :key="n.to"
        :to="n.to"
        class="nav-item-mobile"
        active-class="nav-item-mobile--active"
      >
        <Icon :name="n.icon" :size="22" />
      </RouterLink>
    </nav>
  </div>
</template>
