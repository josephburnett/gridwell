# Gridwell mobile (Flutter)

The third host of the one wasm client. A full-screen webview loads your
server's origin — the same client a plain browser gets — and this shell
adds the one thing a browser can't do: native webviews floated over the
pane content boxes, so url tiles go live on descent like the desktop app.
Same `window.gridwell` bridge contract; declared caps `{liveUrl: true}`.
Shells need no host half: the PTY rides a WebSocket on the web door.

No sidecar: the app connects to a `gridwell serve` you run elsewhere
(typically over a tailnet). First launch asks for the server address;
the server's own login page handles the password and the webview keeps
the cookie.

## Layout

- `lib/bridge.dart` — the bridge core, deliberately Flutter-free:
  the live-view registry, the call dispatcher, and the injected
  `window.gridwell` JS. Everything decision-shaped lives here and runs
  under plain `flutter test`.
- `lib/main.dart` — the shell: server form, host webview, and one
  positioned `InAppWebView` per live view.
- `test/bridge_test.dart` — lifecycle, skew, and JS-contract pins,
  including "every `onX` registrar the wasm installs at boot exists"
  (a missing one is a boot TypeError).

## Building

Flutter SDK required (`flutter doctor`). Then:

    flutter test            # the bridge pins — no device needed
    flutter build apk       # Android (needs the Android SDK)
    flutter build ios       # iOS (needs a Mac + Xcode)

Not yet on-device tested; the dev box has no Android SDK/emulator.
Before a release: verify live-tile place/park/freeze on real hardware,
and the tap-forward focus transfer (`onLeftForward`).
