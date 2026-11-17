import { reactive } from 'vue'
import * as api from '../api/client'

export const styleCompareState = reactive({
  loading: false,
  error: '',
  source: '',
  variants: [] as api.StyleCompareVariant[],
})

export async function runStyleCompare(sessionId: string, text: string) {
  styleCompareState.loading = true
  styleCompareState.error = ''
  styleCompareState.variants = []
  styleCompareState.source = text
  try {
    const resp = await api.styleCompare(sessionId, text)
    styleCompareState.source = resp.source || text
    styleCompareState.variants = resp.variants || []
    if (!styleCompareState.variants.length) {
      styleCompareState.error = '没有其他可用风格'
    }
  } catch (err: any) {
    styleCompareState.error = err?.message || String(err)
  } finally {
    styleCompareState.loading = false
  }
}

export function clearStyleCompare() {
  styleCompareState.loading = false
  styleCompareState.error = ''
  styleCompareState.source = ''
  styleCompareState.variants = []
}
