import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { decide, matchAddress, presentedSite, routeNavigation, type Routing } from './routes';

// routes.ts is a forced copy of rpc.MatchAddress, rpc.Decide and
// rpc.PresentedSite. The cases live once, in api/rpc/testdata/routes.json,
// and api/rpc/routes_fixture_test.go runs the same file against the owners,
// so a rule changed on one side fails the other's build.

const here = dirname(fileURLToPath(import.meta.url)); // apps/desktop/src/main
const FIXTURE = resolve(here, '../../../../api/rpc/testdata/routes.json');

interface Fixture {
  match: { pattern: string; address: string; want: boolean; why?: string }[];
  decide: { routes: Routing['routes']; declared: Routing['declared']; cases: { address: string; plugin?: string; outside?: boolean; broken?: string; why?: string }[] };
  site: { page: string; site: string; location: string; want?: string; ok: boolean; why?: string }[];
}

const fixture = JSON.parse(readFileSync(FIXTURE, 'utf8')) as Fixture;

test('the copy answers every case of the fixture the Go owners answer', () => {
  assert.ok(fixture.match.length > 0 && fixture.decide.cases.length > 0 && fixture.site.length > 0, 'the fixture read no cases');
  for (const c of fixture.match) {
    assert.equal(matchAddress(c.pattern, c.address), c.want, `matchAddress(${c.pattern}, ${c.address}) ${c.why ?? ''}`);
  }
  const routing: Routing = { routes: fixture.decide.routes, declared: fixture.decide.declared };
  for (const c of fixture.decide.cases) {
    assert.deepEqual(
      decide(c.address, routing),
      { plugin: c.plugin ?? '', outside: c.outside ?? false, broken: c.broken ?? '' },
      `decide(${c.address}) ${c.why ?? ''}`,
    );
  }
  for (const c of fixture.site) {
    assert.equal(presentedSite(c.page, c.site, c.location), c.ok ? c.want : null, `presentedSite(${c.location}) ${c.why ?? ''}`);
  }
});

const PAGE = 'http://127.0.0.1:9/content/tok/n1/12/';
const ROUTING: Routing = {
  routes: [
    { match: 'http://example.test/*', plugin: 'pages' },
    { match: 'http://other.test/*', plugin: 'labcode' },
    { match: 'http://outside.test/*', outside: true },
    { match: 'http://down.test/*', plugin: 'down' },
  ],
  declared: {
    pages: { presents: ['http://example.test/*'] },
    labcode: { presents: ['http://other.test/*'] },
    down: { unknown: 'plugin not responding' },
  },
};
const presented = { page: PAGE, site: 'http://example.test/a' };
const plain = { page: PAGE, site: 'http://plain.test/a' };
const served = { page: PAGE, site: '' };

test('a navigation goes where the routes send its site address', () => {
  const cases: [string, string, string, { page: string; site: string }, unknown][] = [
    ['the presenter\'s own page moves in place', PAGE + 'example.test/a', PAGE + 'example.test/b', presented, { kind: 'stay' }],
    ['its assets are its own', PAGE + 'example.test/a', PAGE + '_/app.js', presented, { kind: 'stay' }],
    ['an absolute link to an address it presents loads its page', PAGE + 'example.test/a', 'http://example.test/b', presented, { kind: 'move', site: 'http://example.test/b' }],
    ['an undeclared address in its page goes to the site', PAGE + 'example.test/a', PAGE + 'plain.test/x', presented, { kind: 'move', site: 'http://plain.test/x' }],
    ['a site with no taker leaves the page', PAGE + 'example.test/a', 'http://plain.test/x', presented, { kind: 'move', site: 'http://plain.test/x' }],
    ['another plugin\'s address moves to its page', PAGE + 'example.test/a', 'http://other.test/x', presented, { kind: 'move', site: 'http://other.test/x' }],
    ['outside opens outside from a page', PAGE + 'example.test/a', 'http://outside.test/x', presented, { kind: 'outside', site: 'http://outside.test/x' }],
    ['outside opens outside from the site', 'http://plain.test/a', 'http://outside.test/x', plain, { kind: 'outside', site: 'http://outside.test/x' }],
    ['the plain site moves in place', 'http://plain.test/a', 'http://plain.test/b', plain, { kind: 'stay' }],
    ['the site moves to a presented address\'s page', 'http://plain.test/a', 'http://example.test/b', plain, { kind: 'move', site: 'http://example.test/b' }],
    ['the node\'s own addresses are no site', PAGE + 'example.test/a', 'http://127.0.0.1:9/content/tok/n1/13/x', presented, { kind: 'stay' }],
    ['a non-web address is Chromium\'s', 'http://plain.test/a', 'mailto:a@b.test', plain, { kind: 'stay' }],
    ['a served page moves in its page', PAGE, PAGE + 'next', served, { kind: 'stay' }],
    ['a served page leaves for a site with no taker', PAGE, 'http://plain.test/x', served, { kind: 'stay' }],
    ['a served page opens a presented address below', PAGE, 'http://example.test/b', served, { kind: 'below', site: 'http://example.test/b' }],
    ['a served page sends outside outside', PAGE, 'http://outside.test/x', served, { kind: 'outside', site: 'http://outside.test/x' }],
    ['the site never loads for a broken plugin\'s address', 'http://plain.test/a', 'http://down.test/x', plain, { kind: 'move', site: 'http://down.test/x' }],
    ['nor from a page', PAGE + 'example.test/a', 'http://down.test/x', presented, { kind: 'move', site: 'http://down.test/x' }],
    ['nor in the page', PAGE + 'example.test/a', PAGE + 'down.test/x', presented, { kind: 'move', site: 'http://down.test/x' }],
    ['a served page opens it below, broken', PAGE, 'http://down.test/x', served, { kind: 'below', site: 'http://down.test/x' }],
  ];
  for (const [why, current, target, view, want] of cases) {
    assert.deepEqual(routeNavigation(target, current, view, ROUTING), want, why);
  }
});

test('before the first handshake nothing is routed', () => {
  assert.deepEqual(routeNavigation('http://outside.test/x', 'http://plain.test/a', plain, null), { kind: 'stay' });
});
