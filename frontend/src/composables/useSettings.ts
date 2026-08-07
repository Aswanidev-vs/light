import { ref } from 'vue'
import { SettingsService } from '../../bindings/light/internal/light'
import { listen } from '../lib/events'
import type { Settings } from '../types'

const settings = ref<Settings>({
  deviceName: '',
  port: 9120,
  downloadDir: '',
  autoAccept: false,
  theme: 'dark',
  enableEncryption: false,
})

let inited = false

export function useSettings() {
  async function init() {
    if (inited) return
    inited = true
    settings.value = await SettingsService.GetSettings()
    listen('settings-changed', (s: Settings) => {
      settings.value = s
    })
  }
  return { settings, init }
}
