# Gridwell

Gridwell is a single-tenant personal operating environment: tiles on a 2D
grid, served by a Go backend and normally used through the Electron desktop
app in `apps/desktop`. Start with `CLAUDE.md` (philosophy + engineering
charter) and `ARCHITECTURE.md` (the machine); `apps/desktop/README.md` covers
building, testing, and packaging the desktop shell.

## Browser client / phone access

The desktop app and a plain browser (e.g. your phone) can share **one server
instance** on one stable origin. By default the Electron shell spawns
`gridwell serve` on an ephemeral loopback port (`--bind-default`), unreachable
from other devices. To make it reachable, declare an explicit bind address in
`~/.gridwell/server.yaml`:

```yaml
bind: "100.64.0.7:8080"   # your Tailscale IP
```

An explicit `bind:` always wins over the sidecar's ephemeral default (the
precedence is `--bind` flag > `bind:` in server.yaml > `--bind-default` >
built-in `127.0.0.1:8080`). Then either launch the desktop app as usual — its
window loads from the same origin — or run the server standalone:

```sh
gridwell serve            # honors bind: from ~/.gridwell/server.yaml
```

On the phone, open `http://100.64.0.7:8080` (the same origin the desktop
window uses). What degrades in a plain browser: **live URL tiles stay frozen**
(their live rendering is a native Electron `WebContentsView`; the browser
shows the captured preview). Everything else — the grid, text tiles, wells,
navigation, shells — works.

**Security caveat:** the API is unauthenticated. Anyone who can reach the
bound address can read and write every tile and open live shell PTYs on your
machine. Only bind a VPN-only address (Tailscale is the intended transport);
never an open network interface. `gridwell serve` prints a prominent warning
at startup whenever the resolved bind is not loopback.

## Mounting a remote Gridwell over SSH

A machine already running `gridwell serve` can be mounted into another
machine's Gridwell as an `ssh` plugin: one of the remote's plugins appears on
your launcher like any local plugin — its grids, text tiles, live shells, and
session all cross the tunnel transparently. The remote needs **no extra
setup**: the same one HTTP port that serves browsers also answers the
plugin-scoped gRPC the tunnel carries (the node export), so `bind:
"127.0.0.1:8080"` on the remote is enough — nothing new is exposed to its
network.

On the mounting machine:

```sh
gridwell init --kind ssh --name work \
  --config host=myserver.example:22 \
  --config user=joe \
  --config key=/home/joe/.ssh/id_ed25519 \
  --config known_hosts=/home/joe/.ssh/known_hosts \
  --config addr=127.0.0.1:8080 \
  --config remote_plugin=personal
```

- `addr` is the remote server's `bind:` **as seen on the remote host** —
  loopback is the normal, safest choice; the tunnel reaches it.
- `remote_plugin` picks which of the remote's plugins to mount (its config
  name or uuid). Optional when the remote has exactly one plugin; if it names
  nothing, the error lists what the remote does have.
- `known_hosts` is required — the plugin refuses to trust an unverified host.
- Missing keys fail at `init` time with all of them named at once.
