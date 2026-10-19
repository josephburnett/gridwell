import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { crashFileName, crashText, dumpsDir, LineTail, redact } from './crashlog';

const here = dirname(fileURLToPath(import.meta.url)); // apps/desktop/src/main
const repoRoot = resolve(here, '../../../..');
const goSource = (p: string) => readFileSync(resolve(repoRoot, p), 'utf8');

// The three lines internal/cli/serve.go prints with the web password in them,
// in its own format strings.
const PW = '0123456789abcdef0123456789abcdef';
const TOKEN = 'a'.repeat(64);
const SECRET_LINES = [
  `gridwell: serving on 127.0.0.1:8080 (static=embedded plugins=2 auth=${TOKEN} federation=/h/federation.sock)`,
  `gridwell: already serving on 127.0.0.1:8080 (static=embedded plugins=2 auth=${TOKEN} federation=/h/federation.sock)`,
  `gridwell: web password: ${PW}  (/h/web-password — delete the file to rotate; every browser logs in again)`,
  `gridwell: agents: claude mcp add --transport http gridwell http://127.0.0.1:8080/mcp -H "Authorization: Bearer ${PW}" -H 'X-Gridwell-Shell: \${GRIDWELL_SHELL:-}'`,
];

test('no line the node prints keeps the web password or its token', () => {
  for (const line of SECRET_LINES) {
    const out = redact(line);
    assert.ok(!out.includes(PW) && !out.includes(TOKEN), `kept a secret: ${out}`);
    assert.match(out, /<redacted>/);
  }
  assert.equal(redact('panic: runtime error'), 'panic: runtime error');
});

// The fixtures above are only worth their redaction if serve.go still prints
// these shapes.
test('the redacted shapes are the ones serve.go prints', () => {
  const serve = goSource('internal/cli/serve.go');
  assert.ok(serve.includes('auth=%s'), 'the banner no longer prints auth=');
  assert.ok(serve.includes('gridwell: web password: %s'), 'the password line changed');
  assert.ok(serve.includes('Authorization: Bearer %s'), 'the MCP line changed');
});

test('a tail keeps the last lines, redacted on the way in', () => {
  const t = new LineTail(3);
  for (const l of ['a', 'b', SECRET_LINES[0], 'd']) t.push(l);
  const got = t.lines();
  assert.equal(got.length, 3);
  assert.equal(got[0], 'b');
  assert.ok(!got[1].includes(TOKEN));
  assert.equal(got[2], 'd');
});

// Main writes where the node writes its dumps: config.Dir, then
// config.DumpsDir.
test('the crash file lands in the node directory\'s dumps', () => {
  assert.equal(dumpsDir({ GRIDWELL_HOME: '/n' }, '/home/u'), '/n/dumps');
  assert.equal(dumpsDir({}, '/home/u'), '/home/u/.gridwell/dumps');
  const cfg = goSource('internal/config/config.go');
  assert.ok(cfg.includes('os.Getenv("GRIDWELL_HOME")'), 'config.Dir no longer reads GRIDWELL_HOME');
  assert.ok(cfg.includes('filepath.Join(home, ".gridwell")'), 'config.Dir moved its default');
  assert.ok(cfg.includes('filepath.Join(dir, "dumps")'), 'config.DumpsDir moved');
});

test('a crash file is named for its UTC second, like a trace dump', () => {
  assert.equal(crashFileName(new Date(Date.UTC(2026, 9, 18, 3, 4, 5))), 'crash-20261018-030405.txt');
});

test('a crash file says what died and holds both tails', () => {
  const text = crashText({
    cause: 'sidecar-exit',
    kv: { signal: '', code: '2' },
    when: new Date(Date.UTC(2026, 9, 18)),
    sidecar: ['panic: boom', 'goroutine 1 [running]:'],
    renderer: ['[renderer:error] x'],
  });
  assert.match(text, /^gridwell desktop crash: sidecar-exit\n/);
  assert.match(text, /\ncode: 2\nsignal: \n/);
  assert.match(text, /node output, last 2 lines ---\npanic: boom\ngoroutine 1/);
  assert.match(text, /renderer console, last 1 lines ---\n\[renderer:error\] x\n/);
});
