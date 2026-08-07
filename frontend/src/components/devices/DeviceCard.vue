<script setup lang="ts">
import Icon from '../common/Icon.vue'
import StatusDot from '../common/StatusDot.vue'
import type { Device } from '../../types'

defineProps<{ device: Device; selected?: boolean }>()
defineEmits<{ (e: 'select', id: string): void }>()
</script>

<template>
  <button
    class="flex w-full items-center gap-3 rounded-lg border px-3 py-3 text-left transition no-drag"
    :class="selected ? 'border-accent/50 bg-accent-soft' : 'border-white/5 bg-surface-1 hover:bg-surface-2'"
    @click="$emit('select', device.id)"
  >
    <div class="grid h-10 w-10 place-items-center rounded-lg bg-surface-3 text-content-muted">
      <Icon :name="device.type === 'mobile' ? 'device-mobile' : 'device-desktop'" />
    </div>
    <div class="min-w-0 flex-1">
      <div class="truncate font-medium">{{ device.name }}</div>
      <div class="truncate text-xs text-content-faint">{{ device.address }}</div>
    </div>
    <StatusDot status="online" />
  </button>
</template>
