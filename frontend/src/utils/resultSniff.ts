// resultSniff.ts — classify a tool result string into the
// right export rendering.
//
// A tool's `result` field is an unconstrained string. The
// agent layer never coerces it (so tool authors can return
// rich payloads like base64 PNGs, JSON blobs, or plain
// prose). The export pipeline has to sniff the string to
// decide:
//   - image: render as a markdown image (data: URL) so the
//     base64 actually shows in the rendered file, not as
//     a giant blob inside a code block
//   - json:  render inside a ```json fence so syntax
//     highlighters pick it up; without the language tag
//     most renderers fall back to plain text
//   - code:  multi-line content with code-fence telltale
//     characters — render inside a generic ``` fence
//   - text:  single-line or no obvious structure — render
//     as a blockquote (>)
//
// The sniffers are deliberately conservative: false
// negatives fall back to "text", which is the safe default.
// False positives (calling code "json" when it's not) would
// break the rendered output.

export type ResultKind = 'image' | 'json' | 'code' | 'text'

const DATA_IMAGE_RE = /^data:image\/[a-zA-Z0-9.+-]+;base64,/
const HTTPS_IMAGE_RE = /^https?:\/\/\S+\.(?:png|jpe?g|gif|webp|svg|avif|bmp|tiff?)(?:\?[\S]*)?$/i

/** Cheap shape check: balanced brackets, no leading prose. */
function looksLikeJSON(s: string): boolean {
  const t = s.trim()
  if (!t) return false
  const first = t[0]
  const last = t[t.length - 1]
  if (first !== '{' && first !== '[') return false
  if (last !== '}' && last !== ']') return false
  try {
    JSON.parse(t)
    return true
  } catch {
    return false
  }
}

/** Heuristic: 2+ lines and at least one of `= ( { ; :` →
 *  probably code, not prose. */
function looksLikeCode(s: string): boolean {
  if (!s.includes('\n')) return false
  for (const c of ['=', '(', '{', ';', ':']) {
    if (s.includes(c)) return true
  }
  return false
}

export function sniffResult(s: string | undefined | null): ResultKind {
  if (!s) return 'text'
  if (DATA_IMAGE_RE.test(s) || HTTPS_IMAGE_RE.test(s)) return 'image'
  if (looksLikeJSON(s)) return 'json'
  if (looksLikeCode(s)) return 'code'
  return 'text'
}

/** Format a tool result for markdown export, picking a
 *  rendering appropriate to its sniffed kind. */
export function formatResultForMarkdown(s: string | undefined | null, depth = 0): string {
  const kind = sniffResult(s)
  if (!s) return ''
  const indent = '  '.repeat(depth)
  switch (kind) {
    case 'image':
      // The base64 is already in the string; inline as an
      // image. Markdown renderers (and most viewers) will
      // show the picture.
      return `${indent}![tool result](${s})\n\n`
    case 'json':
      return `${indent}\`\`\`json\n${indent}${s}\n${indent}\`\`\`\n\n`
    case 'code':
      return `${indent}\`\`\`\n${indent}${s}\n${indent}\`\`\`\n\n`
    default:
      // Multi-line prose goes in a blockquote so the
      // markdown structure preserves the indent; single
      // line goes inline.
      if (s.includes('\n')) {
        return s.split('\n').map((l) => `${indent}> ${l}`).join('\n') + '\n\n'
      }
      return `${indent}${s}\n\n`
  }
}
