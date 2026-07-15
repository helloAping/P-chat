import { createApp } from 'vue'
import naive from 'naive-ui'
import { marked, Marked } from 'marked'
import { markedHighlight } from 'marked-highlight'
// P2-2: register highlight.js with marked so code blocks
// render with syntax colours instead of plain text. We use
// the core build + selective language registration to keep
// the bundle small — each language adds ~5-15 KB minified.
// Common aliases (e.g. ts/typescript, py/python, sh/bash)
// are registered under both names so marked's auto-detect
// of `language-xxx` classes from the LLM output hits the
// right entry.
import hljs from 'highlight.js/lib/core'
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import python from 'highlight.js/lib/languages/python'
import go from 'highlight.js/lib/languages/go'
import rust from 'highlight.js/lib/languages/rust'
import java from 'highlight.js/lib/languages/java'
import json from 'highlight.js/lib/languages/json'
import yaml from 'highlight.js/lib/languages/yaml'
import bash from 'highlight.js/lib/languages/bash'
import sql from 'highlight.js/lib/languages/sql'
import xml from 'highlight.js/lib/languages/xml'
import css from 'highlight.js/lib/languages/css'
import markdown from 'highlight.js/lib/languages/markdown'
import App from './App.vue'
import './style.css'

// Language registration. We register each language under
// its primary name plus common aliases so the LLM's
// output ("```ts" / "```typescript" / "```python" /
// "```py") all resolve to the same grammar. Adding a
// new language is a two-line change: import + register
// under each alias the LLM might use.
hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('js', javascript)
hljs.registerLanguage('jsx', javascript)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('ts', typescript)
hljs.registerLanguage('tsx', typescript)
hljs.registerLanguage('python', python)
hljs.registerLanguage('py', python)
hljs.registerLanguage('go', go)
hljs.registerLanguage('golang', go)
hljs.registerLanguage('rust', rust)
hljs.registerLanguage('rs', rust)
hljs.registerLanguage('java', java)
hljs.registerLanguage('json', json)
hljs.registerLanguage('yaml', yaml)
hljs.registerLanguage('yml', yaml)
hljs.registerLanguage('bash', bash)
hljs.registerLanguage('sh', bash)
hljs.registerLanguage('shell', bash)
hljs.registerLanguage('zsh', bash)
hljs.registerLanguage('sql', sql)
hljs.registerLanguage('xml', xml)
hljs.registerLanguage('html', xml)
hljs.registerLanguage('htm', xml)
hljs.registerLanguage('css', css)
hljs.registerLanguage('scss', css)
hljs.registerLanguage('less', css)
hljs.registerLanguage('markdown', markdown)
hljs.registerLanguage('md', markdown)

// Hook into marked via the marked-highlight extension
// (marked v12 removed the inline `highlight` option in
// favour of an extension, which keeps the parser decoupled
// from any specific highlighter). We use a `Marked`
// instance rather than the global `marked` singleton so
// other consumers that import `marked` directly (e.g. the
// MessageBubble's `marked.parse` call) get the highlighter
// too — by setting the same options on the shared
// instance, both code paths produce highlighted output.
//
// The lang-arg is whatever marked extracted from the
// ```lang fence. We trust it when registered, and fall
// back to auto-detect for unknown / missing languages
// so the rendering is robust to LLM hallucination
// ("```javascriptish") without throwing.
const markedInstance = new Marked(
  markedHighlight({
    langPrefix: 'hljs language-',
    highlight(code: string, lang: string): string {
      if (lang && hljs.getLanguage(lang)) {
        try {
          return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value
        } catch {
          // fall through to auto-detect
        }
      }
      try {
        return hljs.highlightAuto(code).value
      } catch {
        // Last-resort: return the raw code escaped. The
        // guard prevents a bad block from breaking the
        // entire message render.
        return code.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      }
    },
  }),
)

// Mirror the configured instance back to the global
// `marked` export so any caller that does
// `import { marked } from 'marked'` and then
// `marked.parse(...)` (e.g. MessageBubble.vue) gets
// the same highlight behaviour. The instance API is
// the source of truth — we just expose its parse fn
// at the global level.
;(marked as any).parse = markedInstance.parse.bind(markedInstance)
;(marked as any).setOptions = markedInstance.setOptions.bind(markedInstance)

const app = createApp(App)
app.use(naive)
app.mount('#app')
