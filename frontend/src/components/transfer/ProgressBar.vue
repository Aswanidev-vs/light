<script setup lang="ts">
defineProps<{ percent: number; active?: boolean; failed?: boolean }>()
</script>

<template>
  <div class="h-1.5 w-full overflow-hidden rounded-full bg-white/10">
    <div
      class="h-full rounded-full transition-all duration-200"
      :class="failed ? 'bg-danger' : active ? 'bg-accent' : 'bg-ok'"
      :style="{ width: Math.max(2, percent) + '%' }"
    />
  </div>
</template>
