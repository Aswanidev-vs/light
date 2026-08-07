<script setup lang="ts">
import { computed } from 'vue'
import { useTransfers } from '../composables/useTransfers'
import FileRow from '../components/transfer/FileRow.vue'
import Icon from '../components/common/Icon.vue'

const { history, clearHistory } = useTransfers()
const items = computed(() =>
  history.value.filter((t) =>
    ['completed', 'failed', 'cancelled'].includes(t.status),
  ),
)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div><div class="page-kicker">Activity</div><h1 class="page-title">History</h1></div>
      <button v-if="items.length" class="btn-ghost text-xs text-danger" @click="clearHistory">
        Clear
      </button>
    </div>

    <p v-if="items.length === 0" class="grid flex-1 place-items-center text-sm text-content-faint">
      No transfer history yet.
    </p>
    <div v-else class="flex flex-1 flex-col gap-2">
      <FileRow v-for="t in items" :key="t.id" :transfer="t" />
    </div>
  </div>
</template>
