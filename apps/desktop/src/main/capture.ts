import type { WebContentsView } from 'electron';

// JPEG quality for mirrored and frozen frames. The frozen preview is the
// durable picture of a url tile: it is what you see without going live, and
// it is zoomed up in larger panes, so it stays crisp. The base64 payload
// over IPC is still modest at this quality.
const JPEG_QUALITY = 92;

// captureJpegBase64 grabs a view's rendered contents as a base64 JPEG for
// IPC to the renderer. capturePage works on the visible attached view, so no
// offscreen-rendering mode is needed. The live pane shows native pixels;
// this capture feeds the other panes' frozen previews and the
// freeze-on-ascend snapshot.
//
// capturePage is time-boxed. A parked or busy renderer can leave the promise
// pending forever, and the freeze path detaches the view only after this
// resolves, which would strand the native view on top of the pane the user
// just left. On timeout it returns '' (no frame) so teardown proceeds.
export async function captureJpegBase64(view: WebContentsView, timeoutMs = 1500): Promise<string> {
  const image = await withTimeout(view.webContents.capturePage(), timeoutMs);
  if (!image || image.isEmpty()) return '';
  return image.toJPEG(JPEG_QUALITY).toString('base64');
}

// withTimeout resolves to null if p hasn't settled within ms (rather than
// rejecting), so callers can treat a slow capture as "no frame" and move on.
function withTimeout<T>(p: Promise<T>, ms: number): Promise<T | null> {
  return new Promise<T | null>((resolve) => {
    let done = false;
    const finish = (v: T | null) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      resolve(v);
    };
    const timer = setTimeout(() => finish(null), ms);
    p.then((v) => finish(v), () => finish(null));
  });
}

// MirrorPump periodically captures every live pane and pushes frames to a
// sink (index.ts sweeps reg.paneIds() each tick), so a tile mirrored in a
// second pane stays fresh. The cadence is modest; mirrored previews do not
// need to run at frame rate.
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
