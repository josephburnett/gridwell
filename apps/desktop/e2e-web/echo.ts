import type { Page } from '@playwright/test';

// Holds the page's Subscribe stream, so a spec can open on demand the window
// a loaded runner opens by chance: the server and the write's answer have
// the change, and the client's cache, which a press hits, does not yet.
const gate = `(() => {
  const orig = window.fetch.bind(window);
  window.__echoHeld = false;
  window.fetch = async (input, init) => {
    const res = await orig(input, init);
    const url = typeof input === 'string' ? input : input.url;
    if (!String(url).endsWith('/gridwell.v1.Gridwell/Subscribe') || !res.body) return res;
    const reader = res.body.getReader();
    const body = new ReadableStream({
      async pull(ctl) {
        const { value, done } = await reader.read();
        while (window.__echoHeld) await new Promise((r) => setTimeout(r, 10));
        if (done) ctl.close();
        else ctl.enqueue(value);
      },
    });
    return new Response(body, { status: res.status, statusText: res.statusText, headers: res.headers });
  };
})()`;

// Installs the gate and reboots the page behind it, because the client opens
// its stream at boot.
export async function installEchoGate(page: Page): Promise<void> {
  await page.addInitScript(gate);
  await page.reload();
  await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
}

// Holds every event from now until ms have passed, on the page's own clock.
export async function holdEchoFor(page: Page, ms: number): Promise<void> {
  await page.evaluate((d) => {
    (window as any).__echoHeld = true;
    setTimeout(() => ((window as any).__echoHeld = false), d);
  }, ms);
}
