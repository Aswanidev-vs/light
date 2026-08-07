import { ref } from 'vue'

const showPair = ref(false)
const toasts = ref<{ id: number; msg: string; type: string }[]>([])
let counter = 0

export function useUI() {
  function toast(msg: string, type = 'info') {
    const id = ++counter
    toasts.value.push({ id, msg, type })
    setTimeout(() => {
      toasts.value = toasts.value.filter((t) => t.id !== id)
    }, 3500)
  }
  return { showPair, toasts, toast }
}
