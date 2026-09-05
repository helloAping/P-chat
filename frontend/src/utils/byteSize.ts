export type ByteSizeUnit = 'B' | 'KB' | 'MB' | 'GB'

export const BYTE_SIZE_FACTORS: Record<ByteSizeUnit, number> = {
  B: 1,
  KB: 1024,
  MB: 1024 ** 2,
  GB: 1024 ** 3,
}

export const byteSizeUnitOptions: Array<{ label: ByteSizeUnit; value: ByteSizeUnit }> = (
  ['B', 'KB', 'MB', 'GB'] as ByteSizeUnit[]
).map(unit => ({ label: unit, value: unit }))

export function preferredByteSizeUnit(bytes: number): ByteSizeUnit {
  if (bytes >= BYTE_SIZE_FACTORS.GB) return 'GB'
  if (bytes >= BYTE_SIZE_FACTORS.MB) return 'MB'
  if (bytes >= BYTE_SIZE_FACTORS.KB) return 'KB'
  return 'B'
}

export function bytesToUnitValue(bytes: number, unit: ByteSizeUnit): number {
  const value = Math.max(0, bytes) / BYTE_SIZE_FACTORS[unit]
  return Number(value.toFixed(unit === 'B' ? 0 : 3))
}

export function unitValueToBytes(value: number, unit: ByteSizeUnit): number {
  return Math.max(1024, Math.round(Math.max(0, value) * BYTE_SIZE_FACTORS[unit]))
}

export function byteSizeInputStep(unit: ByteSizeUnit): number {
  if (unit === 'B') return 1024
  if (unit === 'KB') return 1
  if (unit === 'MB') return 0.25
  return 0.1
}

export function byteSizeInputMinimum(unit: ByteSizeUnit): number {
  if (unit === 'B') return 1024
  if (unit === 'KB') return 1
  return 0.001
}
