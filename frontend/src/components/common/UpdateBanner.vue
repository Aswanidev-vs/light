<script setup lang="ts">
import Icon from './Icon.vue'
import { useUpdater } from '../../composables/useUpdater'

const { info, phase, error, progress, updateAvailable, install, restart, openRelease, dismiss } = useUpdater()

function progressPercent() {
  if (!progress.value.total) return 0
  return Math.min(100, Math.round((progress.value.written / progress.value.total) * 100))
}

function formatSize(bytes = 0) {
  if (!bytes) return ''
  if (bytes < 1024 * 1024) return `${Math.ceil(bytes / 1024)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function phaseLabel() {
  if (phase.value === 'downloading') return `Downloading update · ${progressPercent()}%`
  if (phase.value === 'verifying') return 'Verifying download…'
  if (phase.value === 'installing') return 'Preparing restart…'
  if (phase.value === 'ready') return 'Update ready to apply'
  if (phase.value === 'error') return error.value
  return info.value?.canInstall ? 'A newer version is ready' : 'A newer release is available'
}
</script>

<template>
  <Transition name="update-banner">
    <section
      v-if="updateAvailable || phase === 'downloading' || phase === 'verifying' || phase === 'installing' || phase === 'ready' || (phase === 'error' && info?.available)"
      class="update-banner glass"
      aria-live="polite"
    >
      <div class="flex items-start gap-3">
        <div class="update-banner__mark">
          <Icon :name="phase === 'error' ? 'warning' : phase === 'ready' ? 'check' : 'download'" :size="18" />
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex items-start justify-between gap-3">
            <div>
              <p class="text-[10px] font-semibold uppercase tracking-[0.18em] text-accent">Light update</p>
              <h2 class="mt-1 text-sm font-semibold text-content">
                {{ phase === 'error' ? 'Update paused' : phase === 'ready' ? `Light ${info?.version || ''} is ready` : `Light ${info?.version || 'has a new release'}` }}
              </h2>
            </div>
            <button class="btn-ghost -mr-2 -mt-2 min-h-8 px-2" aria-label="Dismiss update notification" @click="dismiss">
              <Icon name="close" :size="16" />
            </button>
          </div>

          <p class="mt-1 text-xs leading-relaxed text-content-muted">{{ phaseLabel() }}</p>

          <div v-if="phase === 'downloading'" class="mt-3 h-1 overflow-hidden rounded-full bg-white/10">
            <div class="h-full rounded-full bg-accent transition-[width] duration-200" :style="{ width: `${progressPercent()}%` }" />
          </div>

          <div v-if="phase === 'available' || phase === 'error'" class="mt-3 flex flex-wrap items-center gap-2">
            <button v-if="info?.canInstall && phase !== 'error'" class="btn-accent min-h-9 px-3 text-xs" @click="install">
              <Icon name="download" :size="15" />
              Update now
            </button>
            <button v-if="phase === 'error' && info?.canInstall" class="btn-accent min-h-9 px-3 text-xs" @click="install">
              Try again
            </button>
            <button v-if="phase !== 'error'" class="btn-ghost min-h-9 px-3 text-xs" @click="openRelease">
              <Icon name="link" :size="15" />
              View release
            </button>
          </div>
          <div v-else-if="phase === 'ready'" class="mt-3 flex flex-wrap items-center gap-2">
            <button class="btn-accent min-h-9 px-3 text-xs" @click="restart">
              <Icon name="refresh" :size="15" />
              Restart to apply
            </button>
            <button class="btn-ghost min-h-9 px-3 text-xs" @click="openRelease">View release</button>
          </div>
          <p v-if="info?.artifactSize && phase === 'available'" class="mt-2 text-[10px] text-content-faint">
            {{ formatSize(info.artifactSize) }} download · verified before install
          </p>
        </div>
      </div>
    </section>
  </Transition>
</template>

<style scoped>
.update-banner {
  position: fixed;
  top: max(1rem, var(--app-inset-top));
  right: max(1rem, var(--app-inset-right));
  z-index: 150;
  width: min(27rem, calc(100vw - 2rem));
  padding: 1rem;
  border-color: rgba(240, 165, 0, 0.24);
  box-shadow: 0 18px 48px rgba(0, 0, 0, 0.34), 0 0 0 1px rgba(240, 165, 0, 0.04);
}

.update-banner__mark {
  display: grid;
  width: 2.25rem;
  height: 2.25rem;
  flex: 0 0 auto;
  place-items: center;
  border-radius: 0.75rem;
  color: #f0a500;
  background: rgba(240, 165, 0, 0.13);
}

.update-banner-enter-active,
.update-banner-leave-active {
  transition: opacity 0.22s ease, transform 0.22s ease;
}

.update-banner-enter-from,
.update-banner-leave-to {
  opacity: 0;
  transform: translateY(-0.5rem) scale(0.98);
}
</style>
