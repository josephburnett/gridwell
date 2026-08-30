// Pure, Electron-free formatting for the one sidecar-lifecycle notice that
// crosses EV.error. Kept out of sidecar.ts, which imports Electron's `app`
// through ./paths, so `node --test` can exercise the text without an
// Electron runtime. lines.ts is split out for the same reason.

// sidecarExitMessage formats the notice shown when the Go backend exits after
// it was already up and serving. A boot-time failure takes another path:
// startSidecar's promise rejects and boot() in index.ts shows
// dialog.showErrorBox before exiting, since no renderer exists yet to draw a
// notice into. After boot the wasm app keeps running, so the message asks for
// a restart rather than implying the whole app died.
export function sidecarExitMessage(code: number | null, signal: string | null): string {
  const cause = signal ? `signal ${signal}` : `code ${code ?? 'unknown'}`;
  return `backend exited (${cause}) — restart the app`;
}
