import * as net from 'node:net';

// freePort asks the OS for an ephemeral port, then releases it. There is a
// small window between the release and the sidecar's bind, but this is a
// single-user local app on loopback: a collision is very unlikely, and a
// sidecar bind failure surfaces through startSidecar.
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
