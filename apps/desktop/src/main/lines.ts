// Reading the sidecar's stdout. Kept in its own module, with no Electron
// import, so `node --test` can exercise it.

// The address the serve banner announces. host is the raw listener host and may
// be a wildcard, since Go announces a wildcard bind as "[::]:<port>".
interface ServingAddr {
  host: string;
  port: number;
  auth?: string;
  // Another process holds this home's serve lock (internal/cli/servelock.go),
  // so the exited probe child is not the server.
  external?: boolean;
}

// parseServingLine extracts the bound address from the serve banner, or null.
// servingBanner in internal/cli/serve.go owns the banner's shape:
//   "gridwell: serving on <host>:<port> (static=... plugins=N auth=<hex> federation=<socket path>)"
// federation= is not read: the desktop app reaches everything over the web API.
// "already serving on" is the same banner re-emitted by a serve that found the
// lock held, and its address and auth belong to the running holder.
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
  const auth = /\bauth=([0-9a-f]{64})\b/.exec(line)?.[1];
  const out: ServingAddr = { host, port };
  if (auth) out.auth = auth;
  if (external) out.external = true;
  return out;
}

// windowOrigin maps the announced address to the origin the window loads. A
// wildcard is loopback here; a concrete host is kept, so the desktop window and
// a phone browser share one origin.
export function windowOrigin(a: ServingAddr): string {
  return `http://${reachableHost(a)}:${a.port}`;
}

function reachableHost(a: ServingAddr): string {
  const wildcard = a.host === '' || a.host === '0.0.0.0' || a.host === '::';
  return wildcard ? '127.0.0.1' : a.host.includes(':') ? `[${a.host}]` : a.host;
}

// `cb` runs once per newline-delimited line, buffered across chunk boundaries.
// `.flush()` emits a trailing unterminated line.
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
