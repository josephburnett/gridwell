// Pure, electron-free helpers for reading the sidecar's stdout. Kept in
// their own module so `node --test` can exercise them without booting
// Electron.

// isReadyLine reports whether a sidecar log line signals "HTTP listener
// up". `gridwell serve` prints "gridwell: serving on 127.0.0.1:<port> (...)".
export function isReadyLine(line: string): boolean {
  return /gridwell: serving on /.test(line);
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
