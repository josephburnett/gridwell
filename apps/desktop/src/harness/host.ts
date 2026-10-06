// The window a harness places its views in: the app's kind (window.ts), shown
// only once Chromium has composited it.
import { BrowserWindow } from 'electron';

// The display compositor lives in the GPU process, which on a host with no GPU
// first loads Mesa's llvmpipe to probe GL; from a cold disk that takes
// seconds, and until it is up no view yields a frame. Its arrival is an event,
// ready-to-show, so a scenario's frame budget never pays for it.
const COMPOSITED_BUDGET_MS = 30_000;

export async function hostWindow(): Promise<BrowserWindow> {
  const win = new BrowserWindow({ width: 800, height: 600, show: false });
  const composited = new Promise<void>((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`the host window was not composited within ${COMPOSITED_BUDGET_MS}ms`)),
      COMPOSITED_BUDGET_MS,
    );
    win.once('ready-to-show', () => {
      clearTimeout(timer);
      resolve();
    });
  });
  await win.loadURL('data:text/html,<title>host</title>');
  await composited;
  win.show();
  return win;
}
