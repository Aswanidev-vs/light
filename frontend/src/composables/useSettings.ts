import { ref } from 'vue'
import { SettingsService } from '../../bindings/light/internal/light'
import { listen } from '../lib/events'
import type { Settings } from '../types'

const settings = ref<Settings>({
  deviceName: '',
  port: 9120,
  downloadDir: '',
  downloadDirUri: '',
  autoAccept: false,
  theme: 'dark',
  enableEncryption: false,
  transportMode: 'tcp',
  wifiDirect: false,
})

let inited = false

function normalizeDownloadDir(value: Settings): Settings {
  const wails = typeof window !== 'undefined' ? (window as any).wails : null
  if (
    wails?.platform?.() !== 'android' ||
    !value.downloadDir.startsWith('/tree/') ||
    !value.downloadDirUri ||
    typeof wails.getFolderDisplayName !== 'function'
  ) {
    return value
  }
  const displayName = wails.getFolderDisplayName(value.downloadDirUri)
  return displayName ? { ...value, downloadDir: displayName } : value
}

export function useSettings() {
  async function init() {
    if (inited) return
    inited = true
    settings.value = normalizeDownloadDir(await SettingsService.GetSettings())
    listen('settings-changed', (s: Settings) => {
      settings.value = normalizeDownloadDir(s)
    })
  }
  return { settings, init }
}
