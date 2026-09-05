// dataURLToBlobURL keeps large inline media out of reactive state while
// preserving the actual MIME type Chromium needs to render the preview.
export function dataURLToBlobURL(input: string | undefined): string | undefined {
  if (!input?.startsWith('data:')) return input
  try {
    const commaIndex = input.indexOf(',')
    if (commaIndex <= 5) return input

    const metadata = input.slice(5, commaIndex)
    const metadataParts = metadata.split(';')
    const mime = metadataParts[0] || 'application/octet-stream'
    const payload = input.slice(commaIndex + 1)
    let bytes: Uint8Array<ArrayBuffer>
    if (metadataParts.includes('base64')) {
      const binary = atob(payload)
      bytes = new Uint8Array(binary.length)
      for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
    } else {
      const decoded = new TextEncoder().encode(decodeURIComponent(payload))
      bytes = new Uint8Array(decoded.byteLength)
      bytes.set(decoded)
    }
    return URL.createObjectURL(new Blob([bytes], { type: mime }))
  } catch {
    return input
  }
}
