import { ref } from 'vue'
import { DiscoveryService } from '../../bindings/light/internal/light'
import { SettingsService } from '../../bindings/light/internal/light'
import { DeviceType } from '../../bindings/light/internal/light'
import type { WifiDirectPeer } from '../../bindings/light/internal/light'
import { useUI } from './useUI'
import { useDiscovery } from './useDiscovery'
import type { Device } from '../types'

const peers = ref<WifiDirectPeer[]>([])
const scanning = ref(false)
const connectingId = ref<string | null>(null)
const connectedId = ref<string | null>(null)
// Set when this device became the group owner: the peer's endpoint is unknown
// until it dials us, so the UI explains to start the transfer from the other
// device. The peer appears in the regular device list once the link is up.
const ownerNotice = ref(false)
const ownerId = ref<string | null>(null)
const supported = ref(false)

// The Go backend surfaces two sentinel errors we want to present with
// friendly, user-facing copy instead of the raw error string.
function isUnsupported(msg: string): boolean {
  const m = msg.toLowerCase()
  return m.includes('not supported') || m.includes('unsupported')
}
// True once at least one scan has finished, so the panel can distinguish
// "no scan yet" from "scanned and found nothing".
const hasScanned = ref(false)
let probed = false

export function useWifiDirect() {
  const { toast } = useUI()
  const { select, selectedId, refresh, upsert } = useDiscovery()

  async function init() {
    if (probed) return
    probed = true
    try {
      supported.value = await SettingsService.WifiDirectSupported()
    } catch {
      supported.value = false
    }
  }

  async function scan() {
    if (scanning.value) return
    scanning.value = true
    ownerNotice.value = false
    try {
      const found = await DiscoveryService.WifiDirectPeers()
      peers.value = found || []
    } catch (e: any) {
      peers.value = []
      const msg: string = e?.message || String(e)
      if (isUnsupported(msg)) {
        toast("Wi-Fi Direct isn't available on this device / link", 'error')
      } else {
        toast(msg || 'Wi-Fi Direct scan failed', 'error')
      }
    } finally {
      scanning.value = false
      hasScanned.value = true
    }
  }

  async function connect(peer: WifiDirectPeer) {
    if (connectingId.value) return
    connectingId.value = peer.id
    try {
      const addr = await DiscoveryService.ConnectWifiDirect(peer.id, peer.name)
      connectedId.value = peer.id
      // The backend also emits device-found; upsert here too so the device is
      // selected immediately without waiting for the event round-trip.
      const dev: Device = {
        id: peer.id,
        name: peer.name || 'Wi-Fi Direct peer',
        type: DeviceType.DeviceTypeDesktop,
        address: addr,
        code: '',
        lastSeen: new Date(),
      }
      upsert(dev)
      if (selectedId.value !== peer.id) select(peer.id)
      peers.value = []
      toast('Wi-Fi Direct link is up', 'success')
    } catch (e: any) {
      const msg: string = e?.message || String(e)
      if (msg.toLowerCase().includes('group owner')) {
        // Link formed, but as group owner our peer's address is only known
        // once it connects to us. The usual LAN beacons flow across the fresh
        // link, so the peer shows up in the device list shortly. Keep the
        // group alive and offer a disconnect so it can still be torn down.
        ownerId.value = peer.id
        ownerNotice.value = true
        refresh().catch(() => {})
      } else if (isUnsupported(msg)) {
        toast("Wi-Fi Direct isn't available on this device / link", 'error')
      } else {
        toast(msg || 'Wi-Fi Direct connection failed', 'error')
      }
    } finally {
      connectingId.value = null
    }
  }

  async function disconnect(peerId?: string) {
    const id = peerId || connectedId.value || ownerId.value
    if (!id) return
    try {
      await DiscoveryService.DisconnectWifiDirect(id)
    } catch {
      // best-effort teardown
    }
    if (connectedId.value === id) connectedId.value = null
    if (ownerId.value === id) {
      ownerId.value = null
      ownerNotice.value = false
    }
  }

  return { peers, scanning, connectingId, connectedId, ownerNotice, ownerId, hasScanned, supported, init, scan, connect, disconnect }
}
