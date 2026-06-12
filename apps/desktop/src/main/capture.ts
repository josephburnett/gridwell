import type { WebContentsView } from 'electron';

// JPEG quality for mirrored/frozen frames. The frozen preview is a small
// canvas-drawn thumbnail in most panes, so mid quality is plenty and keeps
// the base64 payload over IPC light.
const JPEG_QUALITY = 70;

// captureJpegBase64 grabs the current rendered contents of a view as a JPEG,
// base64-encoded for transport over IPC to the renderer. capturePage works
// on the visible, attached view (no offscreen-rendering mode needed) — the
// live pane shows native pixels; this capture only feeds the *other* panes'
// frozen previews and the freeze-on-ascend snapshot.
export async function captureJpegBase64(view: WebContentsView): Promise<string> {
  const image = await view.webContents.capturePage();
  if (image.isEmpty()) return '';
  return image.toJPEG(JPEG_QUALITY).toString('base64');
}

// MirrorPump periodically captures a set of live panes and pushes frames to
// a sink. The renderer asks for mirroring only when a tile is visible in
// more than one pane (the live pane itself renders natively and needs no
// capture), so this stays cheap. Cadence is intentionally modest — mirrored
// previews don't need 60fps.
export class MirrorPump {
  private timer: NodeJS.Timeout | null = null;
  private readonly intervalMs: number;
  private readonly tick: () => Promise<void>;

  constructor(intervalMs: number, tick: () => Promise<void>) {
    this.intervalMs = intervalMs;
    this.tick = tick;
  }

  start(): void {
    if (this.timer) return;
    const loop = async () => {
      await this.tick().catch(() => {});
      if (this.timer) this.timer = setTimeout(loop, this.intervalMs);
    };
    this.timer = setTimeout(loop, this.intervalMs);
  }

  stop(): void {
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = null;
    }
  }
}
