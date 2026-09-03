import { computed, ref } from 'vue'
import { Browser, Events, Updater as RuntimeUpdater } from '@wailsio/runtime'
import { UpdateService } from '../../bindings/light/internal/light'
import type { UpdateInfo } from '../../bindings/light/internal/light'

type UpdatePhase = 'idle' | 'checking' | 'available' | 'downloading' | 'verifying' | 'installing' | 'ready' | 'up-to-date' | 'error'

const info = ref<UpdateInfo | null>(null)
const phase = ref<UpdatePhase>('idle')
const error = ref('')
const progress = ref({ written: 0, total: 0, rate: 0 })
const dismissed = ref(false)
const updateAvailable = computed(() => Boolean(info.value?.available && !dismissed.value))

let initialized = false
let activeCheck: Promise<void> | null = null
const cleanups: (() => void)[] = []

function payload(event: any): any {
  return event && typeof event === 'object' && 'data' in event ? event.data : event
}

function releaseToInfo(release: any): UpdateInfo {
  const current = info.value
  return {
    supported: current?.supported ?? true,
    canInstall: current?.canInstall ?? false,
    currentVersion: current?.currentVersion ?? '',
    available: true,
    version: release?.version,
    name: release?.name,
    notes: release?.notes,
    releaseUrl: release?.metadata?.['github.release.htmlURL'],
    publishedAt: release?.publishedAt,
    artifactSize: release?.artifact?.size,
  }
}

function installEventListeners() {
  cleanups.push(
    Events.On(RuntimeUpdater.Events.CheckStarted, () => {
      phase.value = 'checking'
      error.value = ''
    }),
    Events.On(RuntimeUpdater.Events.UpdateAvailable, (event) => {
      info.value = releaseToInfo(payload(event))
      phase.value = 'available'
      dismissed.value = false
    }),
    Events.On(RuntimeUpdater.Events.NoUpdate, () => {
      if (info.value) {
        info.value = { ...info.value, available: false, version: undefined, name: undefined, notes: undefined }
      }
      phase.value = 'up-to-date'
    }),
    Events.On(RuntimeUpdater.Events.DownloadStarted, () => {
      phase.value = 'downloading'
      progress.value = { written: 0, total: 0, rate: 0 }
    }),
    Events.On(RuntimeUpdater.Events.DownloadProgress, (event) => {
      progress.value = payload(event) || progress.value
      phase.value = 'downloading'
    }),
    Events.On(RuntimeUpdater.Events.Verifying, () => {
      phase.value = 'verifying'
    }),
    Events.On(RuntimeUpdater.Events.Installing, () => {
      phase.value = 'installing'
    }),
    Events.On(RuntimeUpdater.Events.UpdateReady, () => {
      phase.value = 'ready'
    }),
    Events.On(RuntimeUpdater.Events.Error, (event) => {
      const updateError = payload(event)
      error.value = updateError?.message || 'The update could not be completed.'
      phase.value = 'error'
    }),
  )
}

async function check() {
  if (activeCheck) return activeCheck

  phase.value = 'checking'
  error.value = ''
  activeCheck = UpdateService.CheckForUpdates()
    .then((result) => {
      info.value = result
      phase.value = result.available ? 'available' : 'up-to-date'
      dismissed.value = false
    })
    .catch((reason: unknown) => {
      error.value = reason instanceof Error ? reason.message : 'Could not check for updates.'
      phase.value = 'error'
    })
    .finally(() => {
      activeCheck = null
    })

  return activeCheck
}

async function initialize() {
  if (initialized) return
  initialized = true
  installEventListeners()
  await check()
}

async function install() {
  if (!info.value?.available || !info.value.canInstall || phase.value === 'downloading' || phase.value === 'installing') return
  error.value = ''
  try {
    await UpdateService.DownloadAndInstall()
  } catch (reason: unknown) {
    error.value = reason instanceof Error ? reason.message : 'The update could not be downloaded.'
    phase.value = 'error'
  }
}

async function restart() {
  if (phase.value !== 'ready') return
  try {
    await UpdateService.Restart()
  } catch (reason: unknown) {
    error.value = reason instanceof Error ? reason.message : 'The app could not restart into the update.'
    phase.value = 'error'
  }
}

async function openRelease() {
  const url = info.value?.releaseUrl || 'https://github.com/Aswanidev-vs/light/releases/latest'
  await Browser.OpenURL(url)
}

function dismiss() {
  dismissed.value = true
}

export function useUpdater() {
  return {
    info,
    phase,
    error,
    progress,
    updateAvailable,
    initialize,
    check,
    install,
    restart,
    openRelease,
    dismiss,
  }
}

