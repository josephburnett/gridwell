// ShellStreams is the main-process registry of live shell PTY streams — the
// transport that replaced the /rpc/ShellStream WebSocket bridge (owner
// decision 2026-07-26: browser shell access dropped; the renderer's xterm
// gets its bytes over IPC from a gRPC OpenShell stream dialed here).
//
// Lifecycle rules live in THIS module, injected with a dialer, so they run
// under `node --test` with no gRPC and no Electron (charter §5: the decision
// logic is pure; the glue is thin):
//   - open() for a pane replaces that pane's existing stream (close first);
//   - write/resize after close (or before open) are silent no-ops — races
//     between a teardown and an in-flight keystroke are expected, not errors;
//   - an exit event fires AT MOST once per stream, and only while that stream
//     is still the pane's CURRENT one — an unexpected end flips the renderer
//     back to frozen. A local close or a replacement suppresses it: the
//     renderer initiated those and already knows; without the suppression, a
//     replaced stream's late gRPC 'end' would freeze the pane right after
//     its NEW stream attached.

// ShellStreamHandle is what a dialer returns: the write side of one live
// OpenShell stream.
export interface ShellStreamHandle {
  write(data: Uint8Array): void;
  resize(cols: number, rows: number): void;
  // close ends the stream from this side (CloseSend); the dialer must still
  // deliver onEnd exactly once afterwards.
  close(): void;
}

// ShellDialer opens one OpenShell stream bound to tileId. onData delivers PTY
// output; onEnd delivers the stream's end exactly once (message '' for a
// clean end, else the transport/status error text; sessionGone marks the
// server's "this session no longer exists" verdict so the renderer can flip
// the refresh affordance off).
export type ShellDialer = (
  tileId: string,
  cols: number,
  rows: number,
  onData: (data: Uint8Array) => void,
  onEnd: (message: string, sessionGone: boolean) => void,
) => ShellStreamHandle;

export interface ShellExit {
  paneId: string;
  message: string;
  sessionGone: boolean;
}

export class ShellStreams {
  private streams = new Map<string, { handle: ShellStreamHandle; ended: boolean }>();

  constructor(
    private dial: ShellDialer,
    private onData: (paneId: string, data: Uint8Array) => void,
    private onExit: (ev: ShellExit) => void,
  ) {}

  open(paneId: string, tileId: string, cols: number, rows: number): void {
    this.close(paneId);
    const entry = { handle: undefined as unknown as ShellStreamHandle, ended: false };
    const handle = this.dial(
      tileId,
      cols,
      rows,
      (data) => {
        // Route by the registry, not the closure: a replaced stream's late
        // bytes must not reach the renderer as the NEW stream's output.
        if (this.streams.get(paneId) === entry) this.onData(paneId, data);
      },
      (message, sessionGone) => {
        if (entry.ended) return; // exactly-once, whatever raced
        entry.ended = true;
        if (this.streams.get(paneId) !== entry) return; // replaced/closed: renderer already knows
        this.streams.delete(paneId);
        this.onExit({ paneId, message, sessionGone });
      },
    );
    entry.handle = handle;
    this.streams.set(paneId, entry);
  }

  write(paneId: string, data: Uint8Array): void {
    this.streams.get(paneId)?.handle.write(data);
  }

  resize(paneId: string, cols: number, rows: number): void {
    this.streams.get(paneId)?.handle.resize(cols, rows);
  }

  close(paneId: string): void {
    const entry = this.streams.get(paneId);
    if (!entry) return;
    this.streams.delete(paneId);
    entry.ended = true; // suppress the exit report: this side asked
    entry.handle.close();
  }

  closeAll(): void {
    for (const paneId of [...this.streams.keys()]) this.close(paneId);
  }

  paneIds(): string[] {
    return [...this.streams.keys()];
  }
}
