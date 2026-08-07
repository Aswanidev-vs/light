<script setup lang="ts">
import { computed } from 'vue'
import DeviceList from '../components/devices/DeviceList.vue'
import TransferArea from '../components/transfer/TransferArea.vue'
import { useDiscovery } from '../composables/useDiscovery'
import Icon from '../components/common/Icon.vue'

const { selected, select } = useDiscovery()
</script>

<template>
  <div class="flex h-full">
    <!-- Device list (desktop always visible; mobile only when nothing selected) -->
    <div
      class="w-full border-r border-white/5 bg-surface-0/40 p-5 md:w-80 md:shrink-0 md:p-6"
      :class="selected ? 'hidden md:block' : 'block'"
    >
      <DeviceList />
    </div>

    <!-- Transfer area -->
    <div class="flex-1" :class="selected ? 'block' : 'hidden md:block'">
      <div v-if="selected" class="flex h-full flex-col">
        <div class="flex items-center gap-2 border-b border-white/5 px-4 py-3 md:hidden">
          <button class="btn-ghost px-2" @click="select(selected.id)" aria-label="Back">
            <Icon name="back" />
          </button>
          <span class="font-medium">{{ selected.name }}</span>
        </div>
        <TransferArea :device="selected" />
      </div>
      <div v-else class="grid h-full place-items-center bg-[radial-gradient(circle_at_50%_40%,rgba(240,165,0,0.07),transparent_34%)] p-8 text-center text-content-faint">
        <div>
          <Icon name="send" :size="40" class="mx-auto mb-3 opacity-40" />
          <p>Select a device to start sending files.</p>
        </div>
      </div>
    </div>
  </div>
</template>
