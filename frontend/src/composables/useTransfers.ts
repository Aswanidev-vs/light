import { ref } from 'vue'
import { FileTransferService, TransferManager } from '../../bindings/light/internal/light'
import { listen } from '../lib/events'
import { useUI } from './useUI'
import { useSettings } from './useSettings'
import { useDiscovery } from './useDiscovery'
import type { Device, Transfer, PendingReceive } from '../types'
import { DeviceType, TransferStatus } from '../../bindings/light/internal/light'

const { toast } = useUI()

// UI-only phase: on Android the finished download is copied into the SAF
// folder after the network transfer completes. Shown as a "Saving" row with
// live copy progress so the user never sees 100% while the copy still runs.
const STATUS_SAVING = 'saving' as TransferStatus

const transfers = ref<Transfer[]>([])
const history = ref<Transfer[]>([])
const pendingReceive = ref<PendingReceive | null>(null)
const lastIncomingPeer = ref<Device | null>(null)
let inited = false

const discovery = useDiscovery()

function rememberIncomingPeer(payload: any) {
  if (!payload?.senderAddr) return
  const peer: Device = {
    id: payload.senderId || payload.senderAddr,
    name: payload.senderName || 'Light device',
    type: payload.senderType === 'mobile' ? DeviceType.DeviceTypeMobile : DeviceType.DeviceTypeDesktop,
    address: payload.senderAddr,
    code: '',
    lastSeen: new Date(),
  }
  lastIncomingPeer.value = peer
  discovery.rememberPeer(peer)
}

function upsert(t: Partial<Transfer> & { id: string }) {
  const i = transfers.value.findIndex((x) => x.id === t.id)
  if (i >= 0) {
    transfers.value[i] = { ...transfers.value[i], ...t }
  } else {
    transfers.value.unshift(t as Transfer)
  }
  updateWifiLock()
}

function mark(id: string, status: Transfer['status']) {
  const i = transfers.value.findIndex((x) => x.id === id)
  if (i >= 0) transfers.value[i] = { ...transfers.value[i], status }
  updateWifiLock()
}

// Hold the Wi-Fi radio in high-perf mode only while transfers are in flight;
// otherwise Android downshifts it into power-save and caps throughput. Cached
// so the JS bridge is only crossed when the busy state actually changes.
let wifiLockHeld = false
function updateWifiLock() {
  const wails = (window as any).wails
  if (wails?.platform?.() !== 'android') return
  const busy = transfers.value.some((t) =>
    [TransferStatus.StatusActive, TransferStatus.StatusPaused, STATUS_SAVING].includes(t.status),
  )
  if (busy === wifiLockHeld) return
  wifiLockHeld = busy
  if (busy) wails.acquireTransferWifiLock?.()
  else wails.releaseTransferWifiLock?.()
}

export function useTransfers() {
  async function init() {
    if (inited) return
    inited = true

    try {
      const h = await TransferManager.GetHistory(100)
      history.value = (h || []).filter((t): t is Transfer => t != null)
    } catch {
      history.value = []
    }
    listen('transfer-progress', (p: any) => {
      upsert({
        id: p.id,
        filename: p.filename,
        size: p.size,
        transferred: p.transferred,
        percent: p.size ? Math.round((p.transferred * 100) / p.size) : 0,
        speed: p.speed,
        status: TransferStatus.StatusActive,
      })
    })
    listen('transfer-complete', (p: any) => {
      rememberIncomingPeer(p)
      const previous = transfers.value.find((t) => t.id === p.id)
      const size = p.size ?? previous?.size ?? 0
      // On Android the receiver stages files app-internal (scoped storage
      // blocks raw writes to shared storage). Move the finished file into the
      // SAF folder the user picked via the native bridge; the UI shows a
      // "Saving" phase fed by android:copyProgress until it finishes.
      const plat = (window as any).wails?.platform?.()
      const needsSAFCopy = plat === 'android' && p.destinationUri && p.filePath
      upsert({
        id: p.id,
        filename: p.filename,
        status: needsSAFCopy ? STATUS_SAVING : TransferStatus.StatusCompleted,
        size,
        transferred: needsSAFCopy ? 0 : size,
        percent: needsSAFCopy ? 0 : 100,
        filePath: p.filePath || previous?.filePath,
      })
      if (needsSAFCopy) {
        ;(window as any).wails.copyToFolder(
          JSON.stringify({
            uri: p.destinationUri,
            fileName: p.filename,
            sourcePath: p.filePath,
            transferId: p.id,
          }),
        )
      }
    })
    listen('transfer-error', (p: any) => {
      upsert({ id: p.id, filename: p.filename, status: TransferStatus.StatusFailed, error: p.error })
    })
    listen('transfer-paused', (p: any) => mark(p.id, TransferStatus.StatusPaused))
    listen('transfer-resume', (p: any) => mark(p.id, TransferStatus.StatusActive))
    listen('transfer-cancelled', (p: any) => mark(p.id, TransferStatus.StatusCancelled))
    // Android SAF copy of a staged download into the chosen folder.
    listen('android:copyProgress', (p: any) => {
      if (!p?.id) return
      upsert({
        id: p.id,
        status: STATUS_SAVING,
        size: p.size,
        transferred: p.transferred,
        percent: p.size ? Math.round((p.transferred * 100) / p.size) : 0,
      })
    })
    listen('android:copyDone', (p: any) => {
      if (p && p.ok === false) {
        toast(`Save to folder failed: ${p.error || 'unknown'}`, 'error')
        // Fall back to completed: the file was received and remains in the
        // staging folder even though the SAF copy failed.
        mark(p.id, TransferStatus.StatusCompleted)
        return
      }
      if (p?.id) mark(p.id, TransferStatus.StatusCompleted)
    })
    listen('prepare-receive', (p: any) => {
      rememberIncomingPeer(p)
      // On Android, receiving requires a SAF folder: without one the finished
      // file would be stranded in app-internal storage. Reject the transfer
      // and tell the user to set a download folder first.
      const plat = (window as any).wails?.platform?.()
      const { settings } = useSettings()
      if (plat === 'android' && !settings.value.downloadDirUri) {
        FileTransferService.RejectReceive(p.transferId)
        toast('Set a download folder in Settings to receive files', 'error')
        return
      }
      pendingReceive.value = {
        transferId: p.transferId,
        senderId: p.senderId || p.senderAddr || '',
        senderName: p.senderName,
        senderAddr: p.senderAddr || '',
        senderType: p.senderType || 'desktop',
        files: p.files,
        selected: p.files.map((f: any) => f.name),
      }
    })
  }

  async function acceptReceive() {
    if (!pendingReceive.value) return
    await FileTransferService.AcceptReceive(pendingReceive.value.transferId, pendingReceive.value.selected)
    pendingReceive.value = null
  }

  async function rejectReceive() {
    if (!pendingReceive.value) return
    await FileTransferService.RejectReceive(pendingReceive.value.transferId)
    pendingReceive.value = null
  }

  async function cancel(id: string) {
    await FileTransferService.CancelTransfer(id)
  }
  async function pause(id: string) {
    await FileTransferService.PauseTransfer(id)
  }
  async function resume(id: string) {
    await FileTransferService.ResumeTransfer(id)
  }

  async function clearHistory() {
    await TransferManager.ClearHistory()
    history.value = []
  }

  // Completed/failed/cancelled from the live store also flow into history view.
  function syncHistory() {
    history.value = transfers.value.filter((t) =>
      [TransferStatus.StatusCompleted, TransferStatus.StatusFailed, TransferStatus.StatusCancelled].includes(t.status),
    )
  }

  return {
    transfers,
    history,
    pendingReceive,
    lastIncomingPeer,
    init,
    acceptReceive,
    rejectReceive,
    cancel,
    pause,
    resume,
    clearHistory,
    syncHistory,
  }
}
