<script setup lang="ts">
import Icon from '../common/Icon.vue'
import ProgressBar from './ProgressBar.vue'
import { formatBytes, formatSpeed } from '../../lib/format'
import type { Transfer } from '../../types'

defineProps<{ transfer: Transfer }>()
defineEmits<{
  (e: 'pause', id: string): void
  (e: 'resume', id: string): void
  (e: 'cancel', id: string): void
}>()

const statusLabel: Record<string, string> = {
  pending: 'Waiting',
  active: 'Sending',
  paused: 'Paused',
  completed: 'Done',
  failed: 'Failed',
  cancelled: 'Cancelled',
}
</script>

<template>
  <div class="card grid grid-cols-[auto_minmax(0,1fr)] gap-3 p-3 sm:grid-cols-[auto_minmax(0,1fr)_auto] sm:items-center sm:p-3.5">
    <div class="grid h-10 w-10 place-items-center rounded-lg bg-surface-2 text-content-muted sm:h-11 sm:w-11">
      <Icon name="file" />
    </div>
    <div class="min-w-0 flex-1">
      <div class="flex items-center justify-between gap-2">
        <span class="truncate font-medium">{{ transfer.filename }}</span>
        <span class="shrink-0 rounded-full bg-white/5 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide text-content-faint">{{ statusLabel[transfer.status] || transfer.status }}</span>
      </div>
      <div class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-content-faint">
        <span class="font-mono">{{ formatBytes(transfer.transferred) }} / {{ formatBytes(transfer.size) }}</span>
        <span v-if="transfer.status === 'active'">{{ formatSpeed(transfer.speed) }}</span>
        <span v-if="transfer.status === 'failed'" class="w-full break-words text-danger">{{ transfer.error }}</span>
      </div>
      <div class="mt-2">
        <ProgressBar :percent="transfer.percent" :active="transfer.status === 'active'" :failed="transfer.status === 'failed'" />
      </div>
    </div>
    <div class="col-start-2 flex items-center gap-1 sm:col-start-auto">
      <button
        v-if="transfer.status === 'active'"
        class="btn-ghost px-2"
        aria-label="Pause"
        @click="$emit('pause', transfer.id)"
      >
        <Icon name="pause" :size="16" />
      </button>
      <button
        v-if="transfer.status === 'paused'"
        class="btn-ghost px-2"
        aria-label="Resume"
        @click="$emit('resume', transfer.id)"
      >
        <Icon name="play" :size="16" />
      </button>
      <button
        v-if="transfer.status === 'active' || transfer.status === 'paused'"
        class="btn-ghost px-2 text-danger"
        aria-label="Cancel"
        @click="$emit('cancel', transfer.id)"
      >
        <Icon name="cancel" :size="16" />
      </button>
    </div>
  </div>
</template>
