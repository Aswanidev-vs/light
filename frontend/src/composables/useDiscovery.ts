import { computed, ref } from 'vue'
import { DiscoveryService } from '../../bindings/light/internal/light'
import { listen } from '../lib/events'
import type { Device } from '../types'

const devices = ref<Device[]>([])
const selectedId = ref<string | null>(null)
const remembered = ref<Record<string, Device>>({})
const refreshing = ref(false)
let inited = false

const rememberedStorageKey = 'light:paired-devices'

function saveRemembered() {
  if (typeof localStorage === 'undefined') return
  localStorage.setItem(rememberedStorageKey, JSON.stringify(Object.values(remembered.value)))
}

function loadRemembered() {
  if (typeof localStorage === 'undefined') return
  try {
    const saved = JSON.parse(localStorage.getItem(rememberedStorageKey) || '[]')
    if (!Array.isArray(saved)) return
    for (const raw of saved) {
      if (!raw || typeof raw.id !== 'string' || typeof raw.address !== 'string' || !raw.address) continue
      const peer: Device = {
        id: raw.id,
        name: typeof raw.name === 'string' ? raw.name : 'Light device',
        type: raw.type === 'mobile' ? raw.type : 'desktop',
        address: raw.address,
        code: typeof raw.code === 'string' ? raw.code : '',
        lastSeen: raw.lastSeen ? new Date(raw.lastSeen) : new Date(0),
      }
      remembered.value[peer.id] = peer
      upsert(peer)
    }
  } catch {
    localStorage.removeItem(rememberedStorageKey)
  }
}

function upsert(d: Device) {
  const i = devices.value.findIndex((x) => x.id === d.id)
  if (i >= 0) {
    devices.value[i] = { ...devices.value[i], ...d }
  } else {
    devices.value.push(d)
  }
  if (remembered.value[d.id]) {
    remembered.value[d.id] = { ...remembered.value[d.id], ...d }
    saveRemembered()
  }
}

function remove(id: string) {
  // Remembered peers stay available for reverse sharing even when their
  // latest discovery beacon has expired. A later beacon refreshes the address.
  if (remembered.value[id]) return
  devices.value = devices.value.filter((x) => x.id !== id)
  if (selectedId.value === id) selectedId.value = null
}

export function useDiscovery() {
  const selected = computed(() => devices.value.find((d) => d.id === selectedId.value) || null)
  const count = computed(() => devices.value.length)

  async function init() {
    if (inited) return
    inited = true
    loadRemembered()
    devices.value = await DiscoveryService.GetDevices()
    for (const peer of Object.values(remembered.value)) upsert(peer)
    listen('device-found', (d: Device) => upsert(d))
    listen('device-lost', (p: { id: string }) => remove(p.id))
  }

  async function refresh() {
    if (refreshing.value) return
    refreshing.value = true
    try {
      devices.value = (await DiscoveryService.Refresh()) || []
      for (const peer of Object.values(remembered.value)) upsert(peer)
    } finally {
      refreshing.value = false
    }
  }

  function select(id: string) {
    selectedId.value = selectedId.value === id ? null : id
  }

  // Used by pairing: add a scanned/entered peer explicitly.
  function addPeer(d: Device) {
    rememberPeer(d)
    selectedId.value = d.id
  }

  function rememberPeer(d: Device) {
    if (!d.id || !d.address) return
    remembered.value[d.id] = d
    saveRemembered()
    upsert(d)
  }

  function forgetPeer(id: string) {
    delete remembered.value[id]
    saveRemembered()
    devices.value = devices.value.filter((x) => x.id !== id)
    if (selectedId.value === id) selectedId.value = null
  }

  return { devices, selectedId, selected, count, refreshing, init, refresh, select, addPeer, rememberPeer, forgetPeer, upsert }
}
