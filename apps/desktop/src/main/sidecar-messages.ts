// Pure, Electron-free formatting for the one sidecar-lifecycle notice that
// crosses EV.error. Kept out of sidecar.ts (which transitively imports
// Electron's `app` via ./paths) so `node --test` can exercise the message
// text without an Electron runtime — the same reason lines.ts is split out.

// sidecarExitMessage formats the notice shown when the Go backend process
// exits AFTER it was already up and serving (a post-boot crash — issue #46
// point 1). Boot-time failures are a different path: startSidecar's promise
// rejects and index.ts's boot() catch shows dialog.showErrorBox before
// exiting, because no renderer exists yet to draw a notice into. A post-boot
// exit is different: the wasm app keeps running, so the message tells the
// user to restart rather than implying the whole app just died.
export function sidecarExitMessage(code: number | null, signal: string | null): string {
  const cause = signal ? `signal ${signal}` : `code ${code ?? 'unknown'}`;
  return `backend exited (${cause}) — restart the app`;
}
