// The mobile half of the window.gridwell bridge: the same contract the Electron
// preload implements, backed by Flutter-hosted native webviews instead of
// WebContentsViews. This file is Flutter-free, using dart:core only, so the
// decision logic — the view registry, the call dispatch, the injected JS — runs
// under plain `flutter test` with no platform channel.
//
// The capability declaration is liveUrl, and that is the whole list. Shells ride
// no host bridge: the PTY is a WebSocket on the web door, which the host webview
// speaks like any other same-origin request, so a phone gets live shells for
// free.

/// Bounds of a live view in CSS px of the host page. That is the same as logical
/// px here, because the host webview is full-screen at 1:1 viewport scale, the
/// same 1:1 the Electron shell relies on.
class Bounds {
  final double x, y, width, height;
  const Bounds(this.x, this.y, this.width, this.height);

  static Bounds fromMap(Map<dynamic, dynamic>? m) {
    if (m == null) return const Bounds(0, 0, 0, 0);
    double d(dynamic v) => v is num ? v.toDouble() : 0;
    return Bounds(d(m['x']), d(m['y']), d(m['width']), d(m['height']));
  }

  @override
  String toString() => 'Bounds($x,$y ${width}x$height)';
}

/// LiveView is one placed url view: the registry's value object the widget layer
/// renders as a positioned native webview.
class LiveView {
  final String paneId;
  final String tileId;
  String url;
  Bounds bounds;
  bool hidden;
  LiveView(this.paneId, this.tileId, this.url, this.bounds, {this.hidden = false});
}

/// FreezeCapture is removeWebview's answer: the final frame and page facts the
/// wasm persists as the frozen preview. Empty fields are legal, since a failed
/// capture degrades and the client tolerates it.
class FreezeCapture {
  final String jpegBase64;
  final String url;
  final String title;
  const FreezeCapture({this.jpegBase64 = '', this.url = '', this.title = ''});

  Map<String, String> toMap() => {
        'jpegBase64': jpegBase64,
        'url': url,
        'title': title,
        // History persistence is a desktop feature; the mobile shell sends
        // none. By contract an empty value on a write leaves the stored history
        // untouched.
        'history': '',
      };
}

/// ViewHost is what the widget layer provides per live view: navigation and
/// capture against the real webview controller. It is injected so BridgeState
/// stays platform-free.
abstract class ViewHost {
  Future<FreezeCapture> capture(String paneId);
  Future<void> goBack(String paneId);
}

/// BridgeState owns the live-view registry and dispatches every window.gridwell
/// call. handleCall is the one writer. The widget layer observes [views] through
/// [onChanged] and renders it verbatim.
class BridgeState {
  final ViewHost host;
  final void Function() onChanged;
  final Map<String, LiveView> _views = {};

  BridgeState({required this.host, required this.onChanged});

  /// Views in placement order: what the Stack renders.
  List<LiveView> get views => List.unmodifiable(_views.values);

  LiveView? view(String paneId) => _views[paneId];

  /// handleCall is the one dispatcher for host-bound bridge calls. Unknown
  /// methods are ignored: an older shell under a newer wasm must degrade rather
  /// than throw, the same skew rule the Electron preload follows.
  Future<Object?> handleCall(String method, Map<dynamic, dynamic> args) async {
    final paneId = (args['paneId'] ?? '') as String;
    switch (method) {
      case 'placeWebview':
        final url = (args['url'] ?? '') as String;
        final b = Bounds.fromMap(args['bounds'] as Map?);
        final existing = _views[paneId];
        if (existing != null) {
          // Reuse: re-navigate only on a different address, so a keep-alive
          // return does not reload.
          existing.bounds = b;
          existing.hidden = false;
          if (url.isNotEmpty && existing.url != url) existing.url = url;
        } else {
          _views[paneId] = LiveView(paneId, (args['tileId'] ?? '').toString(), url, b);
        }
        onChanged();
        return null;
      case 'setBounds':
        _views[paneId]?.bounds = Bounds.fromMap(args['bounds'] as Map?);
        onChanged();
        return null;
      case 'setHidden':
        _views[paneId]?.hidden = (args['hidden'] ?? false) as bool;
        onChanged();
        return null;
      case 'removeWebview':
        final v = _views[paneId];
        if (v == null) return const FreezeCapture().toMap();
        final cap = await host.capture(paneId);
        _views.remove(paneId);
        onChanged();
        return cap.toMap();
      case 'goBack':
        await host.goBack(paneId);
        return null;
      default:
        // setZoom and showMenu are desktop-only; reaching here is legal and
        // inert.
        return null;
    }
  }
}

/// gridwellUserScript is the JS injected into the host webview at document
/// start; it must exist before wasm boot reads window.gridwell. Calls flow out
/// through one flutter_inappwebview handler, 'gridwell', and callbacks flow back
/// in through window.__gwDispatch. Every onX registrar the wasm installs must
/// exist here, even the ones this shell never fires: installWebviewListeners
/// calls them all unconditionally at boot, and a missing one is a boot
/// TypeError.
const String gridwellUserScript = '''
(function () {
  if (window.gridwell) return;
  var listeners = {};
  function call(method, args) {
    return window.flutter_inappwebview.callHandler('gridwell', method, args || {});
  }
  function registrar(name) {
    return function (cb) { listeners[name] = cb; };
  }
  window.__gwDispatch = function (name, ev) {
    var cb = listeners[name];
    if (cb) cb(ev);
  };
  window.gridwell = {
    version: 1,
    caps: { liveUrl: true },
    placeWebview: function (a) { call('placeWebview', a); },
    setBounds: function (a) { call('setBounds', a); },
    setHidden: function (a) { call('setHidden', a); },
    setZoom: function (a) { call('setZoom', a); },
    goBack: function (a) { call('goBack', a); },
    showMenu: function (a) { call('showMenu', a); },
    removeWebview: function (a) { return call('removeWebview', a); },
    onFrame: registrar('onFrame'),
    onNav: registrar('onNav'),
    onRightForward: registrar('onRightForward'),
    onMiddleForward: registrar('onMiddleForward'),
    onLeftForward: registrar('onLeftForward'),
    onOpenBelow: registrar('onOpenBelow'),
    onFreezeURL: registrar('onFreezeURL'),
    onZoomKey: registrar('onZoomKey'),
    onError: registrar('onError')
  };
})();
''';

/// dispatchJS builds the host-page call that fires a registered wasm callback,
/// window.__gwDispatch(name, event). The caller encodes the event object with
/// jsonEncode, so this stays a pure formatter.
String dispatchJS(String name, String jsonEvent) {
  return 'window.__gwDispatch(${_quote(name)}, $jsonEvent);';
}

String _quote(String s) => "'${s.replaceAll("'", r"\'")}'";
