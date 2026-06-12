import * as net from 'node:net';

// freePort asks the OS for an ephemeral port, then releases it. There's a
// small TOCTOU window between releasing and the sidecar binding, but this
// is a single-user local app on loopback — collisions are vanishingly
// unlikely and a sidecar bind failure is surfaced loudly by startSidecar.
export function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.once('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address();
      if (addr && typeof addr === 'object') {
        const port = addr.port;
        srv.close(() => resolve(port));
      } else {
        srv.close(() => reject(new Error('no port from listener')));
      }
    });
  });
}
