import { ref } from 'vue'
import { FileTransferService, TransferManager } from '../../bindings/light/internal/light'
import { listen } from '../lib/events'
import type { Transfer, PendingReceive } from '../types'
import { TransferStatus } from '../../bindings/light/internal/light'

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
      upsert({
        id: p.id,
        filename: p.filename,
        status: TransferStatus.StatusCompleted,
        size: p.size || 0,
        transferred: p.size || 0,
        percent: 100,
        filePath: p.filePath,
      })
    })
    listen('transfer-error', (p: any) => {
      upsert({ id: p.id, filename: p.filename, status: TransferStatus.StatusFailed, error: p.error })
    })
    listen('transfer-paused', (p: any) => mark(p.id, TransferStatus.StatusPaused))
    listen('transfer-resume', (p: any) => mark(p.id, TransferStatus.StatusActive))
    listen('transfer-cancelled', (p: any) => mark(p.id, TransferStatus.StatusCancelled))
    listen('prepare-receive', (p: any) => {
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
