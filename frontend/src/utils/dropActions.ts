import { absolutePathFromFile } from './projectPath'

export type DropKind = 'folder' | 'files' | 'empty'

export interface InspectedDrop {
  kind: DropKind
  /** Absolute folder path when kind === 'folder' (may be empty if OS hid it). */
  folderPath: string
  /** Folder display name from the entry / file name. */
  folderName: string
  /** Regular files (never directories). */
  files: File[]
}

/**
 * Classify a drag payload for Delight-2 handlers.
 * Prefers webkit directory entries; falls back to File.path on WebView2.
 */
export async function inspectDataTransfer(dt: DataTransfer | null): Promise<InspectedDrop> {
  if (!dt) return { kind: 'empty', folderPath: '', folderName: '', files: [] }

  const items = dt.items ? Array.from(dt.items) : []
  for (const item of items) {
    if (item.kind !== 'file') continue
    const entry = (item as DataTransferItem & {
      webkitGetAsEntry?: () => FileSystemEntry | null
    }).webkitGetAsEntry?.()
    if (entry?.isDirectory) {
      const file = item.getAsFile()
      return {
        kind: 'folder',
        folderPath: absolutePathFromFile(file),
        folderName: entry.name || file?.name || 'project',
        files: [],
      }
    }
  }

  const files = dt.files ? Array.from(dt.files) : []
  if (files.length === 1) {
    const only = files[0]
    const path = absolutePathFromFile(only)
    // Explorer folder drops sometimes arrive as a single File with no type
    // and an absolute path that is a directory on disk (no extension).
    if (path && !only.type && !/\.[a-z0-9]{1,8}$/i.test(only.name)) {
      return {
        kind: 'folder',
        folderPath: path,
        folderName: only.name || 'project',
        files: [],
      }
    }
  }

  if (files.length > 0) {
    return { kind: 'files', folderPath: '', folderName: '', files }
  }
  return { kind: 'empty', folderPath: '', folderName: '', files: [] }
}
