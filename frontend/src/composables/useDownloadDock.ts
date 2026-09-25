import { computed, ref } from 'vue'

export type DownloadItem = {
  id: string
  name: string
  path: string
  at: number
}

const items = ref<DownloadItem[]>([])
const open = ref(false)

let seq = 0

export function useDownloadDock() {
  const recent = computed(() => items.value.slice(0, 8))

  function pushDownload(name: string, path: string) {
    const entry: DownloadItem = {
      id: `${Date.now()}-${++seq}`,
      name: name || path.split(/[/\\]/).pop() || '文件',
      path,
      at: Date.now(),
    }
    items.value = [entry, ...items.value.filter((x) => x.path !== path)].slice(0, 12)
    open.value = true
  }

  function dismiss() {
    open.value = false
  }

  function clear() {
    items.value = []
    open.value = false
  }

  function remove(id: string) {
    items.value = items.value.filter((x) => x.id !== id)
    if (items.value.length === 0) open.value = false
  }

  return {
    open,
    recent,
    items,
    pushDownload,
    dismiss,
    clear,
    remove,
  }
}
