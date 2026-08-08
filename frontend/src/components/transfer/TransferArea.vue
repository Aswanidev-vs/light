<script setup lang="ts">
import { computed, ref } from 'vue'
import { Dialogs } from '@wailsio/runtime'
import { FileTransferService } from '../../../bindings/light/internal/light'
import { useTransfers } from '../../composables/useTransfers'
import { useUI } from '../../composables/useUI'
import FileRow from './FileRow.vue'
import Icon from '../common/Icon.vue'
import type { Device } from '../../types'

const props = defineProps<{ device: Device }>()
const { transfers, pause, resume, cancel } = useTransfers()
const { toast } = useUI()
const dragOver = ref(false)

async function pickFiles() {
  try {
    const paths = (await Dialogs.OpenFile({
      Title: 'Select files to send',
      AllowsMultipleSelection: true,
      ShowHiddenFiles: false,
    })) as unknown as string[]
    if (paths && paths.length) send(paths)
  } catch {
    toast('Could not open file picker', 'error')
  }
}

function onDrop(e: DragEvent) {
  dragOver.value = false
  const files = Array.from(e.dataTransfer?.files || [])
  const paths = files.map((f) => (f as any).path).filter(Boolean) as string[]
  if (paths.length) send(paths)
  else toast('Drop files from your file manager to send', 'error')
}

async function send(paths: string[]) {
  try {
    await FileTransferService.SendFiles({
      deviceId: props.device.id,
      deviceAddr: props.device.address,
      filePaths: paths,
    })
  } catch (e: any) {
    toast(e?.message || 'Send failed', 'error')
  } finally {
    if (typeof window !== 'undefined' && (window as any).wails?.platform?.() === 'android') {
      ;(window as any).wails.cleanupPickedFiles?.(JSON.stringify(paths))
    }
  }
}

const hasTransfers = computed(() => transfers.value.length > 0)
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 p-4 sm:gap-5 sm:p-6 lg:p-8 xl:p-10">
    <div
      class="drop-zone flex min-h-44 cursor-pointer touch-manipulation flex-col items-center justify-center rounded-2xl border-2 border-dashed border-white/10 bg-surface-0/30 px-4 py-8 text-center transition hover:border-accent/40 hover:bg-accent-soft/30 sm:min-h-52 sm:px-6 sm:py-12"
      :class="dragOver ? 'border-accent bg-accent-soft' : ''"
      @click="pickFiles"
      @dragover.prevent="dragOver = true"
      @dragleave.prevent="dragOver = false"
      @drop.prevent="onDrop"
    >
      <div class="mb-3 grid h-12 w-12 place-items-center rounded-full bg-surface-2 text-accent sm:h-14 sm:w-14">
        <Icon name="upload" :size="24" />
      </div>
      <p class="min-w-0 max-w-full truncate font-medium">Drop files or click to browse</p>
      <p class="min-w-0 max-w-full truncate text-sm text-content-faint">Sending to {{ device.name }}</p>
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto pr-0.5">
      <p v-if="!hasTransfers" class="grid h-full place-items-center text-sm text-content-faint">No transfers yet</p>
      <div v-else class="flex flex-col gap-2">
        <FileRow
          v-for="t in transfers"
          :key="t.id"
          :transfer="t"
          @pause="pause"
          @resume="resume"
          @cancel="cancel"
        />
      </div>
    </div>
  </div>
</template>
