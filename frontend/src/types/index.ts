import type { Device, Settings, Transfer, TransferStatus, Diagnostics } from '../../bindings/light/internal/light'

export type { Device, Settings, Transfer, TransferStatus, Diagnostics }

export interface PendingFile {
  name: string
  size: number
  checksum: string
}

export interface PendingReceive {
  transferId: string
  senderId: string
  senderName: string
  senderAddr: string
  senderType: string
  files: PendingFile[]
  selected: string[]
}
