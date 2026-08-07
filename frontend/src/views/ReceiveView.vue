<script setup lang="ts">
import { useTransfers } from '../composables/useTransfers'
import { useSettings } from '../composables/useSettings'
import FileRow from '../components/transfer/FileRow.vue'
import Icon from '../components/common/Icon.vue'

const { transfers, pause, resume, cancel } = useTransfers()
const { settings } = useSettings()

const received = transfers.value.filter(
  (t) => t.status === 'completed' || t.status === 'failed' || t.status === 'active',
)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div><div class="page-kicker">Transfers</div><h1 class="page-title">Receive</h1></div>
      <div class="hidden text-right text-xs text-content-faint md:block">Incoming files</div>
    </div>

    <div class="card mb-5 flex items-center gap-3 p-4 md:p-5">
      <div class="grid h-10 w-10 place-items-center rounded-full bg-ok/15 text-ok">
        <Icon name="receive" />
      </div>
      <div class="flex-1">
        <div class="font-medium">Ready to receive</div>
        <div class="text-xs text-content-faint">
          Downloads to {{ settings.downloadDir }} · Port {{ settings.port }}
        </div>
      </div>
      <span class="rounded-full bg-ok/20 px-2 py-0.5 text-xs text-ok">Active</span>
    </div>

    <p v-if="transfers.length === 0" class="grid flex-1 place-items-center text-sm text-content-faint">
      Incoming transfers will appear here.
    </p>
    <div v-else class="flex flex-1 flex-col gap-2">
      <FileRow
        v-for="t in transfers"
        :key="t.id"
        :transfer="t"
        @pause="pause"
        @resume="resume"
        @cancel="cancel"
      />
    </div>
  </div>
</template>
