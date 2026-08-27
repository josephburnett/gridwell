// Pure, electron-free helpers for reading the sidecar's stdout. Kept in
// their own module so `node --test` can exercise them without booting
// Electron.

// ServingAddr is the address announced by the serve banner. host is the raw
// WEB listener host: it may be a wildcard ("0.0.0.0", "::", or "") — Go
// announces a wildcard bind as its dual-stack listener address "[::]:<port>".
// federation is the node door's unix socket path — the gRPC export the
// shell relay dials; since 2026-08-26 it is a 0600 socket, never a TCP
// address, and it is the banner's LAST field (a path may contain spaces).
// auth, when present, is the web-UI auth token (server.AuthToken — the value
// of the gridwell_auth cookie): the server prints it (the door always has a
// password, so this window can authenticate without prompting.
export interface ServingAddr {
  host: string;
  port: number;
  federation: string;
  auth?: string;
  // external: the banner came from the "gridwell: already serving on ..."
  // reprint — a server SOMEONE ELSE started holds this home's serve lock
  // (one serve per home; internal/cli/servelock.go). The app connects to
  // it and must never treat its own exited probe child as the server.
  external?: boolean;
}

// parseServingLine extracts the bound address from the serve banner, or null
// for any other line. This is the boot contract with `gridwell serve`
// (internal/cli/serve.go servingBanner): the server prints
//   "gridwell: serving on <host>:<port> (static=... plugins=N auth=<hex> federation=<socket path>)"
// with the listener's ACTUAL bound address, only once both doors are up. A
// banner without federation= is not a serve banner: the shell relay would
// have nothing to dial, so it is null rather than a half-parsed address.
// The server — not this spawner — owns the "where am I listening" fact,
// because server.yaml `bind:` may override the sidecar's --bind-default
// (one stable origin shared with a phone over e.g. Tailscale).
// "already serving on" is the same banner re-emitted by a serve (or
// `gridwell status`) that found the home's lock held: the address/auth are
// the RUNNING holder's, and external marks that this process is not it.
export function parseServingLine(line: string): ServingAddr | null {
  const m = /^gridwell: (already )?serving on (\S+) /.exec(line);
  if (!m) return null;
  const external = !!m[1];
  const addr = m[2];
  const i = addr.lastIndexOf(':');
  if (i < 0) return null;
  const port = Number(addr.slice(i + 1));
  if (!Number.isInteger(port) || port <= 0 || port > 65535) return null;
  let host = addr.slice(0, i);
  if (host.startsWith('[') && host.endsWith(']')) host = host.slice(1, -1); // net.JoinHostPort IPv6 form
  const federation = /\bfederation=(.+)\)$/.exec(line)?.[1];
  if (!federation) return null;
  const auth = /\bauth=([0-9a-f]{64})\b/.exec(line)?.[1];
  const out: ServingAddr = { host, port, federation };
  if (auth) out.auth = auth;
  if (external) out.external = true;
  return out;
}

// windowOrigin maps the announced address to the origin the local Electron
// window should load. A wildcard host is reachable locally as loopback; a
// concrete host (e.g. a Tailscale IP) is kept as-is so the desktop window and
// a phone browser share one origin.
export function windowOrigin(a: ServingAddr): string {
  return `http://${reachableHost(a)}:${a.port}`;
}

// dialAddr is the gRPC node-export target for the shell transport: the
// federation socket the banner announced, in grpc-js's unix: form —
// whatever the web door is bound to.
export function dialAddr(a: ServingAddr): string {
  return `unix:${a.federation}`;
}

function reachableHost(a: ServingAddr): string {
  const wildcard = a.host === '' || a.host === '0.0.0.0' || a.host === '::';
  return wildcard ? '127.0.0.1' : a.host.includes(':') ? `[${a.host}]` : a.host;
}

// makeLineSplitter returns a function you feed raw stream chunks; it calls
// `cb` once per complete newline-delimited line, buffering partial lines
// across chunk boundaries. Call the returned `.flush()` to emit any
// trailing unterminated line on stream end.
export function makeLineSplitter(cb: (line: string) => void): {
  push: (chunk: string) => void;
  flush: () => void;
} {
  let buf = '';
  return {
    push(chunk: string) {
      buf += chunk;
      let idx: number;
      while ((idx = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, idx).replace(/\r$/, '');
        buf = buf.slice(idx + 1);
        cb(line);
      }
    },
    flush() {
      if (buf.length > 0) {
        cb(buf);
        buf = '';
      }
    },
  };
}
