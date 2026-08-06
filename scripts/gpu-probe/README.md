# GPU render A/B harness (debug / diagnostic — not shipped)

Quantifies how much CPU the WebView2/Chromium **GPU process** spends on
P-Chat's streaming-render patterns, so animation changes can be measured
before shipping.

## Files
- `stream-test.html` — standalone page reproducing the P-Chat streaming
  patterns (whole-bubble pulse, sub-agent shimmer, per-delta sub-text
  re-render). `?mode=current|optimized|idle` switches the CSS/JS.
- `cdp-nav.mjs` — navigate the running probe tab to a mode via the Chrome
  DevTools Protocol (Edge must be launched with `--remote-debugging-port`).

## How to measure
1. Launch an isolated Edge on the current mode (fresh user-data-dir so it
   doesn't join your real Edge session, and `--remote-debugging-port=9222`
   so the tab can be re-navigated):
   ```powershell
   msedge --user-data-dir="$env:TEMP\pchat-gpu-probe" --remote-debugging-port=9222 `
          "file:///D:/develop/project/P-chat/scripts/gpu-probe/stream-test.html?mode=current"
   ```
2. Wait ~15s for it to settle, then sample the GPU/renderer CPU of that
   instance only:
   ```powershell
   powershell -File scripts/probe-webview2.ps1 -Seconds 6 -Dir pchat-gpu-probe
   ```
3. Switch modes in-place (keeps the same instance → same browser process,
   clean A/B): `node scripts/gpu-probe/cdp-nav.mjs optimized`, settle, sample.
4. Read the `gpu` row.

Reference numbers measured 2026-08-06 (Edge 151, Windows, large-bubble page,
~25Hz streaming):
| mode           | gpu   | renderer |
|----------------|-------|----------|
| idle (static)  | ~4%   | ~0.5%    |
| pulse+shimmer (no stream) | ~9.3% | ~9% |
| current        | 11.7% | 20.5%    |
| optimized      |  8.1% | 10.3%    |

## Related fix
`MessageBubble.vue` removed the whole-bubble streaming pulse; `SubAgentCard.vue`
removed the shimmer and routes live sub-agent text through `TypedText`
(textContent); markdown rendering shares one LRU cache
(`frontend/src/utils/markdownCache.ts`).
