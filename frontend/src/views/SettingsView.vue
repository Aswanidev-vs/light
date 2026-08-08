<script setup lang="ts">
import { ref, watch, onUnmounted } from 'vue'
import { useSettings } from '../composables/useSettings'
import { SettingsService } from '../../bindings/light/internal/light'
import { useUI } from '../composables/useUI'
import { Events } from '@wailsio/runtime'
import Icon from '../components/common/Icon.vue'

const { settings } = useSettings()
const { toast } = useUI()

const local = ref({ ...settings.value })
watch(settings, () => {
  local.value = { ...settings.value }
}, { immediate: true })

async function save() {
  try {
    await SettingsService.UpdateSettings(local.value)
    toast('Settings saved', 'success')
  } catch {
    toast('Failed to save settings', 'error')
  }
}

function reset() {
  local.value.downloadDir = settings.value.downloadDir
}

let folderPickerCleanup: (() => void) | null = null

async function pickFolder() {
  // On Android, use the native SAF folder picker via the JS bridge
  if (typeof window !== 'undefined' && (window as any).wails?.platform?.() === 'android') {
    return new Promise<void>((resolve) => {
      // Clean up any previous listener
      if (folderPickerCleanup) {
        folderPickerCleanup()
        folderPickerCleanup = null
      }

      const unsub = Events.On('android:folderPicked', (e: any) => {
        const data = e && typeof e === 'object' && 'data' in e ? e.data : e
        if (data?.path) {
          local.value.downloadDir = data.path
        }
        // Persist the SAF tree URI so finished downloads can be copied into
        // this folder (raw-path writes to shared storage are blocked).
        if (data?.uri) {
          local.value.downloadDirUri = data.uri
        }
        if (data?.error) {
          toast('Failed to pick folder', 'error')
        }
        // Clean up after receiving result
        if (folderPickerCleanup) {
          folderPickerCleanup()
          folderPickerCleanup = null
        }
        resolve()
      })

      folderPickerCleanup = unsub as () => void

      // Launch the native picker
      const callbackId = 'folder_' + Date.now()
      ;(window as any).wails.pickFolder(callbackId)
    })
  }

  // On desktop, use the Wails dialog
  try {
    const { Dialogs } = await import('@wailsio/runtime')
    const result = await Dialogs.OpenFile({
      Title: 'Select download folder',
      Directory: local.value.downloadDir,
      CanChooseDirectories: true,
      CanChooseFiles: false,
    })
    if (result) {
      local.value.downloadDir = result
    }
  } catch {
    // Dialog not available — ignore silently
  }
}

onUnmounted(() => {
  if (folderPickerCleanup) {
    folderPickerCleanup()
  }
})
</script>

<template>
  <div class="page">
    <div class="page-header"><div><div class="page-kicker">Preferences</div><h1 class="page-title">Settings</h1></div></div>

    <div class="card flex w-full max-w-3xl flex-col gap-5 p-4 sm:gap-6 sm:p-5 lg:p-6">
      <!-- Device name -->
      <div>
        <label class="field-label">Device name</label>
        <input
          v-model="local.deviceName"
          class="field-input"
        />
      </div>

      <!-- Download directory -->
      <div>
        <label class="field-label">Download folder</label>
        <div class="flex flex-col gap-2 sm:flex-row">
          <input
            v-model="local.downloadDir"
            class="field-input flex-1 font-mono"
          />
          <button class="btn-ghost w-full border border-white/10 sm:w-auto" @click="pickFolder">Browse</button>
          <button class="btn-ghost w-full border border-white/10 sm:w-auto" @click="reset">Reset</button>
        </div>
      </div>

      <!-- Port -->
      <div>
        <label class="field-label">Port</label>
        <input
          v-model.number="local.port"
          type="number"
          min="1"
          max="65535"
          class="field-input w-full sm:w-32"
        />
      </div>

      <!-- Auto accept -->
      <div class="flex items-center justify-between rounded-lg border border-white/5 bg-surface-0 px-4 py-4">
        <div>
          <div class="text-sm font-medium">Auto-accept files</div>
          <div class="text-xs text-content-faint">Incoming files will be accepted automatically</div>
        </div>
        <button
          class="toggle"
          :class="local.autoAccept ? 'toggle--on' : ''"
          :aria-pressed="local.autoAccept"
          aria-label="Toggle auto-accept files"
          @click="local.autoAccept = !local.autoAccept"
        >
          <span
            class="toggle-thumb"
            :class="local.autoAccept ? 'toggle-thumb--on' : ''"
          />
        </button>
      </div>

      <!-- Save -->
      <button class="btn-accent w-full self-start sm:w-auto" @click="save">Save settings</button>
    </div>
  </div>
</template>
