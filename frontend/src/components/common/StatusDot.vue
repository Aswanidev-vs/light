<script setup lang="ts">
defineProps<{ status?: 'online' | 'offline' | 'transferring' }>()
</script>

<template>
  <span
    class="inline-block h-2 w-2 rounded-full"
    :class="{
      'bg-ok': status === 'online',
      'bg-content-faint': status === 'offline' || !status,
      'bg-accent animate-pulse': status === 'transferring',
    }"
  />
</template>
