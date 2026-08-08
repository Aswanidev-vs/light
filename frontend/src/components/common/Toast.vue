<script setup lang="ts">
import { useUI } from '../../composables/useUI'
import Icon from './Icon.vue'

const { toasts } = useUI()
</script>

<template>
  <div class="pointer-events-none fixed inset-x-0 top-[max(1rem,env(safe-area-inset-top))] z-[200] flex flex-col items-center gap-2 px-4">
    <TransitionGroup name="toast">
      <div
        v-for="t in toasts"
        :key="t.id"
        class="glass pointer-events-auto flex max-w-[calc(100vw-2rem)] items-center gap-2 rounded-lg px-4 py-2 text-sm shadow-lg"
        :class="t.type === 'error' ? 'text-danger' : t.type === 'success' ? 'text-ok' : 'text-content'"
      >
        <Icon v-if="t.type === 'error'" name="warning" :size="16" />
        <Icon v-else-if="t.type === 'success'" name="check" :size="16" />
        <span>{{ t.msg }}</span>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toast-enter-active,
.toast-leave-active {
  transition: all 0.25s ease;
}
.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
