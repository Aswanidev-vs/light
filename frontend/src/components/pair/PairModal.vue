<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { QRCodeService, DiscoveryService } from '../../../bindings/light/internal/light'
import { useDiscovery } from '../../composables/useDiscovery'
import { useUI } from '../../composables/useUI'
import Icon from '../common/Icon.vue'

const { addPeer } = useDiscovery()
const { showPair, toast } = useUI()

const qr = ref('')
const myCode = ref('')
const code = ref('')
const scanning = ref(false)
const videoEl = ref<HTMLVideoElement | null>(null)

let stream: MediaStream | null = null
let raf = 0

;(async () => {
  qr.value = await QRCodeService.GetDeviceQRCode()
  myCode.value = await QRCodeService.GetDevicePairingCode()
})()

function close() {
  stopScan()
  showPair.value = false
}

async function pairByCode() {
  const c = code.value.trim()
  if (!/^\d{6}$/.test(c)) {
    toast('Enter the 6-digit code', 'error')
    return
  }
  const dev = await DiscoveryService.PairByCode(c)
  if (dev) {
    addPeer(dev)
    toast('Device paired', 'success')
    close()
  } else {
    toast('No device with that code found', 'error')
  }
}

async function startScan() {
  scanning.value = true
  try {
    stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } })
    if (videoEl.value) {
      videoEl.value.srcObject = stream
      await videoEl.value.play()
    }
    detect()
  } catch {
    toast('Camera unavailable', 'error')
    scanning.value = false
  }
}

function stopScan() {
  scanning.value = false
  if (stream) {
    stream.getTracks().forEach((t) => t.stop())
    stream = null
  }
  cancelAnimationFrame(raf)
}

async function detect() {
  if (!scanning.value || !videoEl.value) return
  const v = videoEl.value
  if (v.videoWidth > 0) {
    const canvas = document.createElement('canvas')
    canvas.width = v.videoWidth
    canvas.height = v.videoHeight
    const ctx = canvas.getContext('2d')
    if (ctx) {
      ctx.drawImage(v, 0, 0)
      const imageData = ctx.getImageData(0, 0, canvas.width, canvas.height)
      let result: string | null = null
      try {
        const BD = (window as any).BarcodeDetector
        if (BD) {
          const found = await new BD({ formats: ['qr_code'] }).detect(canvas)
          if (found.length) result = found[0].rawValue
        } else {
          const jsQR = (await import('jsqr')).default
          const r = jsQR(imageData.data, imageData.width, imageData.height)
          if (r) result = r.data
        }
      } catch {
        /* ignore decode errors */
      }
      if (result) {
        await onScanResult(result)
        return
      }
    }
  }
  raf = requestAnimationFrame(detect)
}

async function onScanResult(text: string) {
  try {
    const t = JSON.parse(text)
    if (t && t.address) {
      addPeer({
        id: t.id,
        name: t.name,
        type: t.type || 'desktop',
        address: t.address,
        code: '',
        lastSeen: new Date(),
      })
      toast('Device added', 'success')
      close()
    } else {
      toast('Not a Light device code', 'error')
    }
  } catch {
    toast('Not a Light device code', 'error')
  }
}

onBeforeUnmount(stopScan)
</script>

<template>
  <div class="fixed inset-0 z-[1100] grid place-items-center bg-black/50 p-4" @click.self="close">
    <div class="card w-full max-w-md animate-slideUp p-5">
      <div class="mb-4 flex items-center justify-between">
        <h2 class="flex items-center gap-2 font-semibold">
          <Icon name="qrcode" /> Pair a device
        </h2>
        <button class="btn-ghost px-2" @click="close" aria-label="Close"><Icon name="close" /></button>
      </div>

      <div v-if="scanning" class="mb-4">
        <video ref="videoEl" class="aspect-square w-full rounded-lg bg-black" autoplay playsinline muted></video>
        <button class="btn-ghost mt-2 w-full" @click="stopScan">Stop scanning</button>
      </div>

      <div v-else class="flex flex-col items-center gap-3">
        <img v-if="qr" :src="qr" class="h-44 w-44 rounded-lg bg-white p-2" alt="Your QR code" />
        <p class="text-xs text-content-faint">Others scan this to connect</p>
        <div class="rounded-lg bg-surface-2 px-4 py-2 font-mono text-lg tracking-widest">{{ myCode }}</div>
        <button class="btn-ghost w-full" @click="startScan"><Icon name="qrcode" /> Scan a code</button>
      </div>

      <div class="mt-4 border-t border-white/5 pt-4">
        <label class="mb-1 block text-xs text-content-faint">Or enter a 6-digit code</label>
        <div class="flex gap-2">
          <input
            v-model="code"
            inputmode="numeric"
            maxlength="6"
            placeholder="000000"
            class="flex-1 rounded-lg border border-white/10 bg-surface-2 px-3 py-2 font-mono tracking-widest outline-none focus:border-accent"
          />
          <button class="btn-accent" @click="pairByCode">Connect</button>
        </div>
      </div>
    </div>
  </div>
</template>
