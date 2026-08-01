// isCurrentStream identifies the stream instance that still owns a
// session. Keeping this check independent of Vue state lets callers
// reject buffered callbacks from a stopped or replaced request.
export function isCurrentStream(
  stream: { ctrl: AbortController } | undefined,
  ctrl: AbortController,
): boolean {
  return !!stream && stream.ctrl === ctrl && !ctrl.signal.aborted
}
