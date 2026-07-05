// Pure, electron-free helpers for reading the sidecar's stdout. Kept in
// their own module so `node --test` can exercise them without booting
// Electron.

// ServingAddr is the address announced by the serve banner. host is the raw
// listener host: it may be a wildcard ("0.0.0.0", "::", or "") — Go announces
// a wildcard bind as its dual-stack listener address "[::]:<port>".
export interface ServingAddr {
  host: string;
  port: number;
}

// parseServingLine extracts the bound address from the serve banner, or null
// for any other line. This is the boot contract with `gridwell serve`
// (internal/cli/serve.go): the server prints
//   "gridwell: serving on <host>:<port> (static=... plugins=N)"
// with the listener's ACTUAL bound address, only once Listen has succeeded.
// The server — not this spawner — owns the "where am I listening" fact,
// because server.yaml `bind:` may override the sidecar's --bind-default
// (one stable origin shared with a phone over e.g. Tailscale).
export function parseServingLine(line: string): ServingAddr | null {
  const m = /^gridwell: serving on (\S+) /.exec(line);
  if (!m) return null;
  const addr = m[1];
  const i = addr.lastIndexOf(':');
  if (i < 0) return null;
  const port = Number(addr.slice(i + 1));
  if (!Number.isInteger(port) || port <= 0 || port > 65535) return null;
  let host = addr.slice(0, i);
  if (host.startsWith('[') && host.endsWith(']')) host = host.slice(1, -1); // net.JoinHostPort IPv6 form
  return { host, port };
}

// windowOrigin maps the announced address to the origin the local Electron
// window should load. A wildcard host is reachable locally as loopback; a
// concrete host (e.g. a Tailscale IP) is kept as-is so the desktop window and
// a phone browser share one origin.
export function windowOrigin(a: ServingAddr): string {
  const wildcard = a.host === '' || a.host === '0.0.0.0' || a.host === '::';
  const host = wildcard ? '127.0.0.1' : a.host.includes(':') ? `[${a.host}]` : a.host;
  return `http://${host}:${a.port}`;
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
