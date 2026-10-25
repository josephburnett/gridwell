import { test } from 'node:test';
import assert from 'node:assert/strict';
import { openOutside } from './outside';
import type { TraceEvent } from './trace';

function harness(fail?: Error) {
  const opened: string[] = [];
  const records: TraceEvent[] = [];
  const open = async (url: string) => {
    if (fail) throw fail;
    opened.push(url);
  };
  return { opened, records, open, record: (ev: TraceEvent) => records.push(ev) };
}

test('an http(s) address opens outside and is traced', async () => {
  const h = harness();
  await openOutside('https://gitlab.test/a', h.open, h.record);
  assert.deepEqual(h.opened, ['https://gitlab.test/a']);
  assert.deepEqual(h.records, [{ src: 'webview', kind: 'outside', msg: 'https://gitlab.test/a', kv: { verdict: 'opened' } }]);
});

// A route names an address, so anything but the web is refused before the OS
// sees it: a mailto: or file: would launch a handler no page may reach.
test('any other address is refused, traced, and never opened', async () => {
  for (const url of ['mailto:a@b.test', 'file:///etc/passwd', 'javascript:alert(1)', '']) {
    const h = harness();
    await assert.rejects(openOutside(url, h.open, h.record), /only an http\(s\) address opens outside/);
    assert.deepEqual(h.opened, [], url);
    assert.equal(h.records.length, 1, url);
    assert.equal(h.records[0].kv?.verdict, 'refused', url);
    assert.ok(!h.records[0].msg.includes('passwd') && !h.records[0].msg.includes('alert'), `the record carries only the scheme of ${url}`);
  }
});

test('a browser that will not open is traced and rejects with its reason', async () => {
  const h = harness(new Error('no default browser'));
  await assert.rejects(openOutside('http://x.test/', h.open, h.record), /no default browser/);
  assert.equal(h.records[0].kv?.verdict, 'failed');
});
