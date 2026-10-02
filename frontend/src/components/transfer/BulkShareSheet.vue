<script setup lang="ts">
import { computed } from 'vue'
import Icon from '../common/Icon.vue'
import { formatBytes } from '../../lib/format'
import { useBulkShare } from '../../composables/useBulkShare'
import type { Device } from '../../types'

const props = defineProps<{ device: Device }>()
const emit = defineEmits<{ (e: 'send', paths: string[]): void }>()

const {
  open,
  staged,
  category,
  categories,
  picking,
  sizing,
  totalSize,
  sizesKnown,
  close,
  setCategory,
  pick,
  remove,
  clear,
} = useBulkShare()

const count = computed(() => staged.value.length)
const canSend = computed(() => count.value > 0 && !picking.value)

async function onPick() {
  await pick()
}

function onSend() {
  if (!canSend.value) return
  emit('send', staged.value.map((f) => f.source))
}
</script>

<template>
  <div
    v-if="open"
    class="fixed inset-0 z-[1000] flex items-end justify-center bg-black/55 backdrop-blur-sm sm:place-items-center"
    @click.self="close"
  >
    <div
      class="flex max-h-[calc(100dvh-1rem)] w-full max-w-2xl animate-slideUp flex-col overflow-hidden rounded-t-2xl border border-white/5 bg-surface-1 shadow-[0_24px_60px_rgba(0,0,0,0.5)] sm:rounded-2xl"
    >
      <!-- Header -->
      <header class="flex items-center justify-between gap-3 border-b border-white/5 px-5 py-4">
        <div class="flex items-center gap-2.5">
          <span class="grid h-9 w-9 place-items-center rounded-lg bg-accent-soft text-accent">
            <Icon name="upload" :size="18" />
          </span>
          <div>
            <h2 class="text-sm font-semibold">Bulk Share</h2>
            <p class="text-xs text-content-faint">
              Sending to <span class="text-content-muted">{{ device.name }}</span>
            </p>
          </div>
        </div>
        <button class="btn-ghost -mr-2 px-2" aria-label="Close bulk share" @click="close">
          <Icon name="close" />
        </button>
      </header>

      <!-- Category tabs -->
      <nav class="flex gap-1 overflow-x-auto border-b border-white/5 px-3 py-2">
        <button
          v-for="c in categories"
          :key="c.id"
          class="min-h-10 shrink-0 rounded-lg px-3 text-sm font-medium transition"
          :class="
            category === c.id
              ? 'bg-accent-soft text-accent'
              : 'text-content-faint hover:bg-white/5 hover:text-content'
          "
          :aria-pressed="category === c.id"
          @click="setCategory(c.id)"
        >
          {{ c.label }}
        </button>
      </nav>

      <!-- Picker + staged list -->
      <div class="min-h-0 flex-1 overflow-y-auto">
        <button
          class="m-4 flex min-h-24 w-[calc(100%-2rem)] flex-col items-center justify-center gap-1.5 rounded-xl border-2 border-dashed border-white/10 bg-surface-0/40 px-4 py-6 text-center transition hover:border-accent/40 hover:bg-accent-soft/25 disabled:opacity-50"
          :disabled="picking"
          @click="onPick"
        >
          <Icon name="file" :size="22" class="text-accent" />
          <span class="text-sm font-medium">
            {{ picking ? 'Opening picker…' : `Add ${categories.find((c) => c.id === category)?.label.toLowerCase()}` }}
          </span>
          <span class="text-xs text-content-faint">Pick several at once, then add more</span>
        </button>

        <div v-if="count" class="px-4 pb-4">
          <div class="mb-2 flex items-center justify-between">
            <p class="text-xs uppercase tracking-[0.16em] text-content-faint">
              {{ count }} file{{ count === 1 ? '' : 's' }}
              <template v-if="sizesKnown"> · {{ formatBytes(totalSize) }}</template>
              <template v-else-if="sizing"> · sizing…</template>
            </p>
            <button class="text-xs text-content-faint transition hover:text-danger" @click="clear">
              Clear all
            </button>
          </div>
          <ul class="flex flex-col gap-1">
            <li
              v-for="f in staged"
              :key="f.source"
              class="flex items-center gap-2 rounded-lg bg-surface-0/50 px-3 py-2"
            >
              <Icon name="file" :size="16" class="shrink-0 text-content-faint" />
              <span class="min-w-0 flex-1 truncate text-sm" :title="f.name">{{ f.name }}</span>
              <span v-if="f.size >= 0" class="shrink-0 text-xs tabular-nums text-content-faint">
                {{ formatBytes(f.size) }}
              </span>
              <button
                class="shrink-0 rounded p-1 text-content-faint transition hover:text-danger"
                :aria-label="`Remove ${f.name}`"
                @click="remove(f.source)"
              >
                <Icon name="close" :size="14" />
              </button>
            </li>
          </ul>
        </div>

        <p v-else class="px-6 pb-8 text-center text-sm text-content-faint">
          Nothing staged yet. Add files from a category above.
        </p>
      </div>

      <!-- Actions -->
      <footer
        class="flex flex-col gap-2 border-t border-white/5 px-4 py-3 pb-[max(0.75rem,var(--app-inset-bottom))] sm:flex-row sm:items-center sm:justify-between"
      >
        <p class="text-xs text-content-faint">
          <template v-if="count">Each file is sent as soon as it is picked up.</template>
          <template v-else>Select at least one file.</template>
        </p>
        <div class="flex gap-2">
          <button class="btn-ghost flex-1 border border-white/10 sm:flex-none" @click="close">Cancel</button>
          <button class="btn-accent flex-1 disabled:opacity-40 sm:flex-none" :disabled="!canSend" @click="onSend">
            Send {{ count || '' }}
          </button>
        </div>
      </footer>
    </div>
  </div>
</template>