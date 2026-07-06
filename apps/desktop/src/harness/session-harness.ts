// Session-sync integration harness (issue #9). Boots a real Electron main
// process and exercises the cookie round trip that decides whether logins
// survive: dehydratePartition must capture a partition's cookies into the
// blob it PUTs to the sidecar, and hydratePartition must inject a canned
// blob's cookies into a fresh partition. A local HTTP stub stands in for the
// sidecar's /session/<uuid> endpoint so the harness needs no Go process.
//
//   npm run build && xvfb-run -a electron dist/harness/session-harness.js
//
// Prints "HARNESS PASS" / "HARNESS FAIL: ..." and exits with 0/1.
import * as http from 'node:http';
import { app, session as electronSession, BaseWindow, WebContentsView } from 'electron';
import { dehydratePartition, hydratePartition } from '../main/session';
import { partitionFor } from '../main/viewutil';

function fail(msg: string): never {
  console.error('HARNESS FAIL:', msg);
  app.exit(1);
  throw new Error(msg);
}

// sidecarStub serves GET/PUT /session/<uuid> from an in-memory map.
function sidecarStub(blobs: Map<string, Buffer>): Promise<string> {
  return new Promise((resolve) => {
    const srv = http.createServer((req, res) => {
      // A real-origin page for the localStorage round trip (data: URLs have
      // opaque origins where localStorage throws).
      if (req.url === '/page') {
        res.writeHead(200, { 'Content-Type': 'text/html' });
        res.end('<script>try{localStorage.setItem("k","local-sekrit")}catch(e){document.title="LSFAIL"}</script>ok');
        return;
      }
      if (req.url === '/reader') {
        res.writeHead(200, { 'Content-Type': 'text/html' }).end('reader');
        return;
      }
      const uuid = (req.url ?? '').replace('/session/', '');
      if (req.method === 'PUT') {
        const chunks: Buffer[] = [];
        req.on('data', (c) => chunks.push(c));
        req.on('end', () => {
          blobs.set(uuid, Buffer.concat(chunks));
          res.writeHead(200).end();
        });
        return;
      }
      res.writeHead(200).end(blobs.get(uuid) ?? Buffer.alloc(0));
    });
    srv.listen(0, '127.0.0.1', () => {
      const a = srv.address() as { port: number };
      resolve(`http://127.0.0.1:${a.port}`);
    });
  });
}

app.whenReady().then(main).catch((err) => fail(`harness threw: ${err?.stack ?? err}`));

async function main() {
  const blobs = new Map<string, Buffer>();
  const origin = await sidecarStub(blobs);

  // ── dehydrate: a cookie set in the partition must reach the PUT blob ─────
  const uuidA = 'harness-plugin-a';
  const sesA = electronSession.fromPartition(partitionFor(uuidA));
  await sesA.cookies.set({
    url: 'https://example.com/',
    name: 'login',
    value: 'sekrit',
    domain: 'example.com',
    path: '/',
    secure: true,
    expirationDate: Date.now() / 1000 + 3600,
  });
  const errs: string[] = [];
  await dehydratePartition(origin, uuidA, (m) => errs.push(m));
  if (errs.length > 0) fail(`dehydrate reported: ${errs.join('; ')}`);
  const put = blobs.get(uuidA);
  if (!put || put.length === 0) fail('dehydrate PUT no blob');
  const putEnv = JSON.parse(put.toString('utf8')) as { v: number; cookies: Array<{ name: string; value: string }> };
  if (putEnv.v !== 2) fail(`blob is not the v2 envelope: ${put.slice(0, 40)}`);
  const saved = putEnv.cookies.find((c) => c.name === 'login');
  if (!saved || saved.value !== 'sekrit') fail(`login cookie missing from blob: ${put}`);
  console.log(`dehydrate ok: ${putEnv.cookies.length} cookie(s) captured`);

  // ── hydrate: a canned blob's cookies must appear in a FRESH partition ────
  const uuidB = 'harness-plugin-b';
  blobs.set(uuidB, put); // reuse plugin A's blob as the canned session
  await hydratePartition(origin, uuidB, (m) => errs.push(m));
  if (errs.length > 0) fail(`hydrate reported: ${errs.join('; ')}`);
  const sesB = electronSession.fromPartition(partitionFor(uuidB));
  const restored = await sesB.cookies.get({ name: 'login' });
  if (restored.length !== 1 || restored[0].value !== 'sekrit') {
    fail(`hydrated partition has ${restored.length} login cookie(s), want the saved one`);
  }
  console.log('hydrate ok: login cookie restored into a fresh partition');

  // ── failure surfacing: a dead sidecar must report, not swallow ───────────
  const failures: string[] = [];
  const count = () => failures.length; // opaque to TS literal narrowing
  await dehydratePartition('http://127.0.0.1:1', uuidA, (m) => failures.push(m));
  if (count() !== 1 || !failures[0].includes('session save failed')) {
    fail(`dead-sidecar dehydrate did not surface: ${JSON.stringify(failures)}`);
  }
  await hydratePartition('http://127.0.0.1:1', uuidB, (m) => failures.push(m));
  if (count() !== 2 || !failures[1].includes('session restore failed')) {
    fail(`dead-sidecar hydrate did not surface: ${JSON.stringify(failures)}`);
  }
  // An EMPTY blob is the normal never-persisted case — silent by design.
  blobs.set('harness-plugin-c', Buffer.alloc(0));
  const before = count();
  await hydratePartition(origin, 'harness-plugin-c', (m) => failures.push(m));
  if (count() !== before) fail('empty blob wrongly reported as a failure');
  console.log('failure surfacing ok: dead sidecar reports, empty blob stays silent');

  console.log('session ok: cookie round trip preserved through dehydrate → blob → hydrate');

  // ── localStorage round trip (issue #23): full-fidelity via the dir snapshot ─
  // Write localStorage through a REAL page on partition D, dehydrate, then
  // hydrate the blob into partition E (untouched this run) and read it back.
  // Run-unique names: persist: partitions outlive the process on disk, and
  // hydrate's dir restore correctly refuses to overwrite an existing
  // partition — a leftover from a previous harness run would mask the test.
  const run = Date.now().toString(36);
  const uuidD = `harness-d-${run}`;
  const win = new BaseWindow({ width: 400, height: 300, show: true }); // hidden windows throttle loads
  const mkView = (partition: string) =>
    new WebContentsView({ webPreferences: { partition } });
  const viewD = mkView(partitionFor(uuidD));
  win.contentView.addChildView(viewD);
  await viewD.webContents.loadURL(`${origin}/page`);
  await new Promise((r) => setTimeout(r, 300)); // let the write land
  await dehydratePartition(origin, uuidD, (m) => fail(`dehydrate D: ${m}`));
  const blobD = blobs.get(uuidD);
  if (!blobD) fail('no blob for D');
  const env = JSON.parse(blobD.toString('utf8'));
  if (env.v !== 2) fail(`blob is not a v2 envelope: ${blobD.slice(0, 40)}`);
  const fileNames = Object.keys(env.files ?? {});
  if (fileNames.length === 0) fail('v2 blob captured no partition files (localStorage missing)');
  const carriesValue = fileNames.some((f: string) =>
    Buffer.from(env.files[f], 'base64').includes('local-sekrit'));
  console.log(`dir snapshot ok: ${fileNames.length} file(s), value-present=${carriesValue}`);
  if (!carriesValue) fail('the localStorage value never reached the snapshot — flush timing');

  const uuidE = `harness-e-${run}`;
  blobs.set(uuidE, blobD);
  await hydratePartition(origin, uuidE, (m) => fail(`hydrate E: ${m}`));
  const viewE = mkView(partitionFor(uuidE));
  win.contentView.addChildView(viewE);
  await viewE.webContents.loadURL(`${origin}/reader`);
  const restoredLS = await viewE.webContents.executeJavaScript('localStorage.getItem("k")');
  if (restoredLS !== 'local-sekrit') {
    fail(`localStorage after hydrate = ${JSON.stringify(restoredLS)}, want the saved value`);
  }
  console.log('localStorage ok: survived dehydrate → blob → hydrate into a fresh partition');

  console.log('HARNESS PASS');
  app.exit(0);
}
