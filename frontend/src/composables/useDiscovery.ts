import { computed, ref } from 'vue'
import { DiscoveryService } from '../../bindings/light/internal/light'
import { listen } from '../lib/events'
import type { Device } from '../types'

const devices = ref<Device[]>([])
const selectedId = ref<string | null>(null)
let inited = false

function upsert(d: Device) {
  const i = devices.value.findIndex((x) => x.id === d.id)
  if (i >= 0) {
    devices.value[i] = { ...devices.value[i], ...d }
  } else {
    devices.value.push(d)
  }
}

function remove(id: string) {
  devices.value = devices.value.filter((x) => x.id !== id)
  if (selectedId.value === id) selectedId.value = null
}

export function useDiscovery() {
  const selected = computed(() => devices.value.find((d) => d.id === selectedId.value) || null)
  const count = computed(() => devices.value.length)

  async function init() {
    if (inited) return
    inited = true
    devices.value = await DiscoveryService.GetDevices()
    listen('device-found', (d: Device) => upsert(d))
    listen('device-lost', (p: { id: string }) => remove(p.id))
  }

  function select(id: string) {
    selectedId.value = selectedId.value === id ? null : id
  }

  // Used by pairing: add a scanned/entered peer explicitly.
  function addPeer(d: Device) {
    upsert(d)
    selectedId.value = d.id
  }

  return { devices, selectedId, selected, count, init, select, addPeer, upsert }
}
