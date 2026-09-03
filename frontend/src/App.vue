<script setup lang="ts">
import { onMounted } from 'vue'
import AppLayout from './components/layout/AppLayout.vue'
import PairModal from './components/pair/PairModal.vue'
import IncomingRequest from './components/receive/IncomingRequest.vue'
import Toast from './components/common/Toast.vue'
import UpdateBanner from './components/common/UpdateBanner.vue'
import { useDiscovery } from './composables/useDiscovery'
import { useTransfers } from './composables/useTransfers'
import { useSettings } from './composables/useSettings'
import { useUI } from './composables/useUI'
import { useUpdater } from './composables/useUpdater'

const discovery = useDiscovery()
const transfers = useTransfers()
const settings = useSettings()
const { showPair } = useUI()
const { pendingReceive } = useTransfers()
const updater = useUpdater()

onMounted(async () => {
  await settings.init()
  await discovery.init()
  await transfers.init()
  await updater.initialize()
})
</script>

<template>
  <AppLayout>
    <router-view />
  </AppLayout>

  <PairModal v-if="showPair" />
  <IncomingRequest v-if="pendingReceive" />
  <Toast />
  <UpdateBanner />
</template>
