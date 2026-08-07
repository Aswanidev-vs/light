<script setup lang="ts">
import { useDiscovery } from '../../composables/useDiscovery'
import { useUI } from '../../composables/useUI'
import DeviceCard from './DeviceCard.vue'
import Icon from '../common/Icon.vue'

const { devices, selectedId, select } = useDiscovery()
const { showPair } = useUI()
</script>

<template>
  <div class="flex flex-col gap-3">
    <div class="flex items-center justify-between">
      <div>
        <div class="page-kicker">Nearby</div>
        <h2 class="text-lg font-semibold tracking-tight text-content">Devices</h2>
      </div>
      <button class="btn-ghost px-2 py-1 text-xs" @click="showPair = true">
        <Icon name="qrcode" :size="16" />
        <span>Pair</span>
      </button>
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
