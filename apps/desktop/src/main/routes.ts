import { webUrl } from './viewutil';

// Where a live page's navigation goes, decided in main before the page moves,
// because will-navigate is answered synchronously. matchAddress, decide and
// presentedSite are a forced copy of rpc.MatchAddress, rpc.Decide and
// rpc.PresentedSite, the owners, since Go and TypeScript share no source;
// api/rpc/testdata/routes.json pins the copy (routes.test.ts).

// One rule of the handshake's routes, as the renderer hands it over.
interface Route {
  match: string;
  plugin?: string;
  outside?: boolean;
}

// What a plugin declares (rpc.Declared): its presents, or why they are
// unknown.
interface Declared {
  presents?: string[];
  unknown?: string;
}

// The handshake's routes and each plugin's declaration (rpc.Routing).
export interface Routing {
  routes: Route[];
  declared: Record<string, Declared>;
}

interface Verdict {
  plugin: string;
  outside: boolean;
  broken: string;
}

export function matchAddress(pattern: string, address: string): boolean {
  const parts = pattern.split('*');
  if (parts.length === 1) return pattern === address;
  if (!address.startsWith(parts[0])) return false;
  let rest = address.slice(parts[0].length);
  for (const p of parts.slice(1, -1)) {
    const i = rest.indexOf(p);
    if (i < 0) return false;
    rest = rest.slice(i + p.length);
  }
  return rest.endsWith(parts[parts.length - 1]);
}

export function decide(address: string, r: Routing): Verdict {
  for (const route of r.routes) {
    if (!matchAddress(route.match, address)) continue;
    if (route.outside) return { plugin: '', outside: true, broken: '' };
    const plugin = route.plugin ?? '';
    const d: Declared = Object.prototype.hasOwnProperty.call(r.declared, plugin) ? r.declared[plugin] : {};
    if (d.unknown) return { plugin, outside: false, broken: d.unknown };
    if ((d.presents ?? []).some((p) => matchAddress(p, address))) return { plugin, outside: false, broken: '' };
  }
  return { plugin: '', outside: false, broken: '' };
}

export function presentedSite(page: string, site: string, location: string): string | null {
  const i = site.indexOf('://');
  if (i < 0 || !location.startsWith(page)) return null;
  const rest = location.slice(page.length);
  if (rest === '' || rest.startsWith('_/') || '?#'.includes(rest[0])) return null;
  return site.slice(0, i) + '://' + rest;
}

// What main holds of one live view to route it: page is the tile's page on
// the node (rpc.PageURL), site the tile's address, '' for a page its plugin
// serves, which has no address to move.
export interface ViewRoute {
  page: string;
  site: string;
}

// stay lets the page move: the current taker's own navigation. outside opens
// the site in the default browser, move tells the renderer the tile goes to
// the site, and below opens it as a visit below a served page.
export type NavVerdict =
  | { kind: 'stay' }
  | { kind: 'outside' | 'move' | 'below'; site: string };

const STAY: NavVerdict = { kind: 'stay' };

function origin(u: string): string {
  try {
    return new URL(u).origin;
  } catch {
    return '';
  }
}

// siteOf is the site address u names for view: inside its page the one it
// is the page of, a web address outside the node its own, and null for
// anything else, which is no site's.
function siteOf(u: string, view: ViewRoute): string | null {
  if (u.startsWith(view.page)) return presentedSite(view.page, view.site, u);
  if (webUrl(u) === null || origin(u) === origin(view.page)) return null;
  return u;
}

// routeNavigation is where a view at current going to target goes, by the
// routes. No routing yet, before the renderer's first handshake, lets
// everything stay, as before routes existed. A site a broken plugin takes
// never loads in the view: the tile moves to it, and its row reads broken.
export function routeNavigation(target: string, current: string, view: ViewRoute, routing: Routing | null): NavVerdict {
  if (!routing || view.page === '') return STAY;
  const site = siteOf(target, view);
  if (site === null) return STAY;
  const v = decide(site, routing);
  if (v.outside) return { kind: 'outside', site };
  if (view.site === '') return v.plugin ? { kind: 'below', site } : STAY;
  if (v.broken) return { kind: 'move', site };
  const inPage = current.startsWith(view.page);
  const shows = inPage ? decide(siteOf(current, view) ?? '', routing).plugin : '';
  const same = target.startsWith(view.page) ? shows !== '' && v.plugin === shows : v.plugin === '' && !inPage;
  return same ? STAY : { kind: 'move', site };
}
