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
  <div class="flex h-[100dvh] min-h-0 flex-col bg-ink-deep md:flex-row">
    <!-- Tablet rail / desktop sidebar -->
    <aside class="hidden w-[76px] shrink-0 flex-col border-r border-white/5 bg-surface-0 md:flex lg:w-64">
      <div class="drag-region flex min-h-20 items-center border-b border-white/5 px-3 lg:px-5">
        <div class="brand-lockup">
          <img src="/logo.png" alt="Light" class="brand-logo" />
          <div class="hidden min-w-0 lg:block">
            <div class="text-sm font-bold tracking-[0.18em] text-content">LIGHT</div>
            <div class="text-[9px] uppercase tracking-[0.16em] text-content-faint">Local transfer</div>
          </div>
        </div>
      </div>

      <nav class="flex flex-col gap-1 px-2 pt-4 lg:px-3">
        <RouterLink
          v-for="n in nav"
          :key="n.to"
          :to="n.to"
          :title="n.label"
          class="nav-item justify-center lg:justify-start"
          active-class="nav-item--active"
        >
          <Icon :name="n.icon" :size="20" />
          <span class="hidden lg:inline">{{ n.label }}</span>
        </RouterLink>
      </nav>

      <div class="mt-auto border-t border-white/5 p-3">
        <button class="btn-ghost w-full justify-center lg:justify-start" @click="showPair = true">
          <Icon name="qrcode" :size="20" />
          <span class="hidden lg:inline">Pair device</span>
        </button>
        <div class="mt-3 hidden items-center gap-2 px-2 text-xs text-content-faint lg:flex">
          <span class="h-2 w-2 rounded-full" :class="count ? 'bg-ok' : 'bg-content-faint'"></span>
          {{ count }} device{{ count === 1 ? '' : 's' }} nearby
        </div>
      </div>
    </aside>

    <!-- Mobile header -->
    <header class="mobile-header drag-region flex min-h-16 items-center justify-between border-b border-white/5 bg-surface-0 px-4 pb-3 md:hidden">
      <div class="brand-lockup">
        <img src="/logo.png" alt="Light" class="brand-logo h-8 w-8 rounded-lg" />
        <div>
          <div class="text-xs font-bold tracking-[0.18em] text-content">LIGHT</div>
          <div class="text-[9px] uppercase tracking-[0.14em] text-content-faint">Local transfer</div>
        </div>
      </div>
      <button class="btn-ghost" @click="showPair = true" aria-label="Pair device">
        <Icon name="qrcode" :size="20" />
      </button>
    </header>

    <!-- Content -->
    <main class="min-h-0 flex-1 overflow-hidden">
      <slot />
    </main>

    <!-- Mobile bottom nav -->
    <nav class="mobile-nav grid grid-cols-4 border-t border-white/5 bg-surface-0 md:hidden">
      <RouterLink
        v-for="n in nav"
        :key="n.to"
        :to="n.to"
        class="nav-item-mobile"
        active-class="nav-item-mobile--active"
      >
        <Icon :name="n.icon" :size="20" />
        <span class="truncate">{{ n.label }}</span>
      </RouterLink>
    </nav>
  </div>
</template>
