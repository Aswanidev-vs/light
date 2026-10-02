import { ref, computed } from 'vue'
import { Dialogs } from '@wailsio/runtime'
import type { FileFilter } from '@wailsio/runtime/types/dialogs'
import { FileTransferService } from '../../bindings/light/internal/light'

export type PickCategory = 'images' | 'video' | 'audio' | 'documents' | 'games'

interface Category {
  id: PickCategory
  label: string
  icon: string
  /** MIME types sent to Android's SAF picker via EXTRA_MIME_TYPES. */
  mimeTypes: string[]
  /** Extension patterns used as the native desktop dialog filter. */
  patterns: string
}

const CATEGORIES: Category[] = [
  { id: 'images', label: 'Images', icon: 'file', mimeTypes: ['image/*'], patterns: '*.jpg;*.jpeg;*.png;*.webp;*.gif;*.heic;*.bmp' },
  { id: 'video', label: 'Video', icon: 'file', mimeTypes: ['video/*'], patterns: '*.mp4;*.mkv;*.mov;*.webm;*.avi;*.m4v' },
  { id: 'audio', label: 'Audio', icon: 'file', mimeTypes: ['audio/*'], patterns: '*.mp3;*.m4a;*.flac;*.wav;*.ogg;*.aac' },
  {
    id: 'documents',
    label: 'Documents',
    icon: 'file',
    mimeTypes: ['application/pdf', 'application/msword', 'text/*'],
    patterns: '*.pdf;*.doc;*.docx;*.txt;*.md;*.csv;*.xls;*.xlsx;*.ppt;*.pptx',
  },
  {
    id: 'games',
    label: 'Games',
    icon: 'file',
    // Installers, split APKs/OBB and mod archives cover what people actually
    // sideload; the provider treats an unrecognised type as a match anyway.
    mimeTypes: [
      'application/vnd.android.package-archive',
      'application/zip',
      'application/x-obb+zip',
      'application/octet-stream',
    ],
    patterns: '*.apk;*.apks;*.xapk;*.obb;*.zip;*.7z;*.rar;*.iso;*.pak;*.dat',
  },
]

export interface StagedFile {
  source: string
  name: string
  /** -1 until the backend reports it, or if it could not be resolved. */
  size: number
}

const UNKNOWN_SIZE = -1

const open = ref(false)
const staged = ref<StagedFile[]>([])
const category = ref<PickCategory>('images')
const picking = ref(false)
const sizing = ref(false)

export function useBulkShare() {
  const categories = CATEGORIES

  const totalSize = computed(() => staged.value.reduce((n, f) => n + Math.max(0, f.size), 0))
  // Native pickers hand back paths only, so a source the backend cannot stat
  // must not be summed as zero bytes.
  const sizesKnown = computed(() => staged.value.length > 0 && staged.value.every((f) => f.size >= 0))

  function show() {
    open.value = true
  }
  function close() {
    open.value = false
  }

  function setCategory(id: PickCategory) {
    category.value = id
  }

  function remove(source: string) {
    const i = staged.value.findIndex((f) => f.source === source)
    if (i >= 0) staged.value.splice(i, 1)
  }

  function clear() {
    staged.value = []
  }

  /**
   * Append picked sources and resolve their real sizes. The native dialogs only
   * return paths, so sizes come from the backend's StatFiles.
   */
  async function stage(sources: string[]) {
    const fresh = sources.filter((s) => s && !staged.value.some((f) => f.source === s))
    if (!fresh.length) return
    for (const source of fresh) {
      staged.value.push({ source, name: displayName(source), size: UNKNOWN_SIZE })
    }
    sizing.value = true
    try {
      const stats = await FileTransferService.StatFiles(fresh)
      for (let i = 0; i < fresh.length; i++) {
        const target = staged.value.find((f) => f.source === fresh[i])
        if (!target) continue
        const stat = stats?.[i]
        if (stat && !stat.error) {
          target.size = stat.size
          target.name = stat.name || target.name
        }
      }
    } catch {
      // Sizes stay unknown; the sheet hides the total rather than lying.
    } finally {
      sizing.value = false
    }
  }

  /** Pick a category's files. Returns the sources so the caller can send them. */
  async function pick(): Promise<string[]> {
    if (picking.value) return []
    picking.value = true
    const cat = CATEGORIES.find((c) => c.id === category.value) || CATEGORIES[0]
    try {
      const filters: FileFilter[] = [{ DisplayName: cat.label, Pattern: cat.patterns }]
      const paths = (await Dialogs.OpenFile({
        Title: `Select ${cat.label.toLowerCase()} to send`,
        AllowsMultipleSelection: true,
        ShowHiddenFiles: false,
        // Android ignores Filters and routes through the SAF category picker,
        // which the native layer maps onto EXTRA_MIME_TYPES.
        Filters: filters,
      })) as unknown as string[]

      if (!paths?.length) return []
      await stage(paths)
      return paths
    } finally {
      picking.value = false
    }
  }

  return {
    open,
    staged,
    category,
    categories,
    picking,
    sizing,
    totalSize,
    sizesKnown,
    show,
    close,
    setCategory,
    pick,
    remove,
    clear,
  }
}

function displayName(source: string): string {
  if (!source.startsWith('lightpick://')) {
    const parts = source.split(/[\\/]/)
    return parts[parts.length - 1] || source
  }
  // lightpick://<host>/<token>/<id>/<percent-encoded name>
  const tail = source.slice(source.indexOf('lightpick://') + 'lightpick://'.length)
  const segments = tail.split('/')
  return decodeURIComponent(segments[segments.length - 1] || 'file')
}