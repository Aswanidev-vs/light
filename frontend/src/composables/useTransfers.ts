import { ref } from 'vue'
import { FileTransferService, TransferManager } from '../../bindings/light/internal/light'
import { listen } from '../lib/events'
import { useUI } from './useUI'
import { useSettings } from './useSettings'
import type { Transfer, PendingReceive } from '../types'
import { TransferStatus } from '../../bindings/light/internal/light'

const { toast } = useUI()

const transfers = ref<Transfer[]>([])
const history = ref<Transfer[]>([])
const pendingReceive = ref<PendingReceive | null>(null)
let inited = false

function upsert(t: Partial<Transfer> & { id: string }) {
  const i = transfers.value.findIndex((x) => x.id === t.id)
  if (i >= 0) {
    transfers.value[i] = { ...transfers.value[i], ...t }
  } else {
    transfers.value.unshift(t as Transfer)
  }
}

function mark(id: string, status: Transfer['status']) {
  const i = transfers.value.findIndex((x) => x.id === id)
  if (i >= 0) transfers.value[i] = { ...transfers.value[i], status }
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
      const previous = transfers.value.find((t) => t.id === p.id)
      const size = p.size ?? previous?.size ?? 0
      upsert({
        id: p.id,
        filename: p.filename,
        status: TransferStatus.StatusCompleted,
        size,
        transferred: size,
        percent: 100,
        filePath: p.filePath || previous?.filePath,
      })
      // On Android the receiver stages files app-internal (scoped storage
      // blocks raw writes to shared storage). Move the finished file into the
      // SAF folder the user picked via the native bridge.
      const plat = (window as any).wails?.platform?.()
      if (plat === 'android' && p.destinationUri && p.filePath) {
        ;(window as any).wails.copyToFolder(
          JSON.stringify({ uri: p.destinationUri, fileName: p.filename, sourcePath: p.filePath }),
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
    listen('android:copyDone', (p: any) => {
      if (p && p.ok === false) {
        toast(`Save to folder failed: ${p.error || 'unknown'}`, 'error')
      }
    })
    listen('prepare-receive', (p: any) => {
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
        senderName: p.senderName,
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
