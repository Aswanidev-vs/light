<script setup lang="ts">
import { useTransfers } from '../../composables/useTransfers'
import { useUI } from '../../composables/useUI'
import Icon from '../common/Icon.vue'
import { formatBytes } from '../../lib/format'

const { pendingReceive, acceptReceive, rejectReceive } = useTransfers()
const { toast } = useUI()

function toggle(name: string) {
  const pr = pendingReceive.value
  if (!pr) return
  const i = pr.selected.indexOf(name)
  if (i >= 0) pr.selected.splice(i, 1)
  else pr.selected.push(name)
}

async function accept() {
  await acceptReceive()
  toast('Accepting files…', 'success')
}
async function reject() {
  await rejectReceive()
  toast('Transfer declined', 'info')
}
</script>

<template>
  <div
    v-if="pendingReceive"
    class="fixed inset-0 z-[1000] flex items-end justify-center bg-black/50 p-4 pb-[max(1rem,env(safe-area-inset-bottom))] sm:grid sm:place-items-center"
    @click.self="reject"
  >
    <div class="card max-h-[calc(100dvh-1rem)] w-full max-w-md animate-slideUp overflow-y-auto p-5">
      <div class="mb-1 flex items-center gap-2 text-accent">
        <Icon name="receive" />
        <span class="font-semibold">Incoming files</span>
      </div>
      <p class="mb-4 text-sm text-content-muted">
        from <span class="font-medium text-content">{{ pendingReceive.senderName }}</span>
      </p>

      <ul class="mb-4 flex max-h-60 flex-col gap-1 overflow-auto">
        <li
          v-for="f in pendingReceive.files"
          :key="f.name"
          class="flex items-center gap-2 rounded-lg px-2 py-2 hover:bg-white/5"
        >
          <input
            type="checkbox"
            :checked="pendingReceive.selected.includes(f.name)"
            class="accent-accent"
            @change="toggle(f.name)"
          />
          <span class="min-w-0 flex-1 truncate text-sm">{{ f.name }}</span>
          <span class="shrink-0 text-xs text-content-faint">{{ formatBytes(f.size) }}</span>
        </li>
      </ul>

      <div class="flex flex-col gap-2 sm:flex-row">
        <button class="btn-ghost order-2 flex-1 border border-white/10 sm:order-1" @click="reject">Decline</button>
        <button class="btn-accent order-1 flex-1 sm:order-2" @click="accept">Accept {{ pendingReceive.selected.length }}</button>
      </div>
    </div>
  </div>
</template>
