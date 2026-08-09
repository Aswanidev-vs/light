<script setup lang="ts">
import { ref } from 'vue'
import { useDiscovery } from '../../composables/useDiscovery'
import { useUI } from '../../composables/useUI'
import DeviceCard from './DeviceCard.vue'
import Icon from '../common/Icon.vue'

const { devices, selectedId, select, refresh, refreshing } = useDiscovery()
const { showPair, toast } = useUI()
const pullRoot = ref<HTMLElement | null>(null)
const pullStartY = ref(0)
const pullDistance = ref(0)
const isPulling = ref(false)
const pullThreshold = 64

function onTouchStart(event: TouchEvent) {
  const scrollHost = pullRoot.value?.parentElement
  if (refreshing.value || (scrollHost && scrollHost.scrollTop > 0)) return
  pullStartY.value = event.touches[0]?.clientY || 0
  isPulling.value = true
}

function onTouchMove(event: TouchEvent) {
  if (!isPulling.value) return
  const distance = (event.touches[0]?.clientY || 0) - pullStartY.value
  if (distance <= 0) {
    pullDistance.value = 0
    return
  }
  pullDistance.value = Math.min(distance * 0.5, 96)
  event.preventDefault()
}

async function onTouchEnd() {
  const shouldRefresh = pullDistance.value >= pullThreshold
  isPulling.value = false
  pullDistance.value = 0
  if (!shouldRefresh) return
  await refreshDevices()
}

async function refreshDevices() {
  try {
    await refresh()
  } catch (error: any) {
    toast(error?.message || 'Could not refresh devices', 'error')
  }
}
</script>

<template>
  <div
    ref="pullRoot"
    class="flex flex-col gap-3"
    @touchstart="onTouchStart"
    @touchmove="onTouchMove"
    @touchend="onTouchEnd"
    @touchcancel="onTouchEnd"
  >
    <div
      class="flex items-center justify-center gap-2 overflow-hidden text-xs text-content-faint transition-[height] duration-150"
      :style="{ height: refreshing ? '2rem' : `${pullDistance}px` }"
      aria-live="polite"
    >
      <Icon name="refresh" :size="14" :class="refreshing ? 'animate-spin' : ''" />
      <span>{{ refreshing ? 'Refreshing devices…' : pullDistance >= pullThreshold ? 'Release to refresh' : 'Pull to refresh' }}</span>
    </div>
    <div class="flex items-center justify-between">
      <div>
        <div class="page-kicker">Nearby</div>
        <h2 class="text-lg font-semibold tracking-tight text-content">Devices</h2>
      </div>
      <div class="flex items-center gap-1">
        <button class="btn-ghost px-2 py-1 text-xs" :disabled="refreshing" aria-label="Refresh devices" @click="refreshDevices">
          <Icon name="refresh" :size="16" :class="refreshing ? 'animate-spin' : ''" />
        </button>
        <button class="btn-ghost px-2 py-1 text-xs" @click="showPair = true">
          <Icon name="qrcode" :size="16" />
          <span>Pair</span>
        </button>
      </div>
    </div>

    <p
      v-if="devices.length === 0"
      class="rounded-lg border border-dashed border-white/10 px-4 py-6 text-center text-sm text-content-faint"
    >
      Searching for devices on your network…
    </p>

    <div v-else class="flex flex-col gap-2">
      <DeviceCard
        v-for="d in devices"
        :key="d.id"
        :device="d"
        :selected="d.id === selectedId"
        @select="select"
      />
    </div>
  </div>
</template>
