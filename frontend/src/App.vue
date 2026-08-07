<script setup lang="ts">
import { onMounted } from 'vue'
import AppLayout from './components/layout/AppLayout.vue'
import PairModal from './components/pair/PairModal.vue'
import IncomingRequest from './components/receive/IncomingRequest.vue'
import Toast from './components/common/Toast.vue'
import { useDiscovery } from './composables/useDiscovery'
import { useTransfers } from './composables/useTransfers'
import { useSettings } from './composables/useSettings'
import { useUI } from './composables/useUI'

const discovery = useDiscovery()
const transfers = useTransfers()
const settings = useSettings()
const { showPair } = useUI()
const { pendingReceive } = useTransfers()

onMounted(async () => {
  await settings.init()
  await discovery.init()
  await transfers.init()
})
</script>

<template>
  <AppLayout>
    <router-view />
  </AppLayout>

  <PairModal v-if="showPair" />
  <IncomingRequest v-if="pendingReceive" />
  <Toast />
</template>
