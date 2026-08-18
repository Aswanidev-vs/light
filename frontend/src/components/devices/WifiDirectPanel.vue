<script setup lang="ts">
import { onMounted } from 'vue'
import { useWifiDirect } from '../../composables/useWifiDirect'
import { useSettings } from '../../composables/useSettings'
import Icon from '../common/Icon.vue'
import StatusDot from '../common/StatusDot.vue'

const { settings } = useSettings()
const {
  peers,
  scanning,
  connectingId,
  connectedId,
  ownerNotice,
  ownerId,
  hasScanned,
  supported,
  init,
  scan,
  connect,
  disconnect,
} = useWifiDirect()

onMounted(() => init())
</script>

<template>
  <section v-if="supported && settings.wifiDirect" class="rounded-lg border border-white/5 bg-surface-1/60 p-3">
    <div class="mb-2 flex items-center justify-between">
      <div class="flex items-center gap-2">
        <Icon name="wifi" :size="15" class="text-accent" />
        <span class="text-xs font-semibold uppercase tracking-wider text-content-muted">Wi-Fi Direct</span>
      </div>
      <button
        class="btn-ghost px-2 py-1 text-xs"
        :disabled="scanning || !!connectingId"
        aria-label="Scan for Wi-Fi Direct peers"
        @click="scan"
      >
        <Icon name="refresh" :size="14" :class="scanning ? 'animate-spin' : ''" />
        <span class="hidden sm:inline">{{ scanning ? 'Scanning…' : 'Scan' }}</span>
      </button>
    </div>

    <p v-if="ownerNotice" class="mb-2 rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs leading-relaxed text-warn">
      You became the group owner — start the transfer from the other device. The peer will appear in the normal device list via LAN beacons.
    </p>

    <div v-if="connectedId || ownerId" class="mb-2 flex items-center justify-between rounded-md border border-ok/25 bg-ok/10 px-3 py-2">
      <div class="flex min-w-0 items-center gap-2 text-xs">
        <StatusDot status="online" />
        <span class="truncate text-ok">{{ connectedId ? 'Connected to peer' : 'Group formed — you are the group owner' }}</span>
      </div>
      <button class="btn-ghost shrink-0 px-2 py-1 text-xs" @click="disconnect()">Disconnect</button>
    </div>

    <div v-if="scanning && peers.length === 0" class="rounded-md border border-dashed border-white/10 px-3 py-3 text-center text-xs text-content-faint">
      Searching for nearby devices…
    </div>

    <div v-else-if="peers.length === 0" class="px-1 py-2 text-xs text-content-faint">
      <template v-if="hasScanned">
        No Wi-Fi Direct peers found. Make sure the other device has Light open, then Scan again.
      </template>
      <template v-else>
        Scan to find nearby devices over a private Wi-Fi Direct link.
      </template>
    </div>

    <div v-else class="flex flex-col gap-1.5">
      <button
        v-for="p in peers"
        :key="p.id"
        class="flex w-full items-center gap-3 rounded-md border px-3 py-2 text-left transition no-drag"
        :class="connectingId === p.id ? 'border-accent/50 bg-accent-soft' : 'border-white/5 bg-surface-2 hover:bg-surface-3'"
        :disabled="!!connectingId"
        @click="connect(p)"
      >
        <div class="grid h-8 w-8 shrink-0 place-items-center rounded-md bg-surface-3 text-content-muted">
          <Icon name="device-mobile" :size="16" />
        </div>
        <div class="min-w-0 flex-1">
          <div class="truncate text-sm font-medium">{{ p.name || p.id }}</div>
          <div class="truncate font-mono text-[10px] text-content-faint">{{ p.id }}</div>
        </div>
        <span class="shrink-0 text-xs" :class="connectingId === p.id ? 'text-accent' : 'text-content-faint'">
          {{ connectingId === p.id ? 'Connecting…' : 'Connect' }}
        </span>
      </button>
    </div>
  </section>
</template>
