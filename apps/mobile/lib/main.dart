// The Gridwell mobile shell: the third host of the one wasm client. A
// full-screen host webview loads the server's origin, the same client a plain
// browser gets, and this shell adds what a browser cannot: native webviews
// floated over the pane content boxes for live url tiles, driven through the
// same window.gridwell bridge contract Electron implements. The declared
// capability is liveUrl only; shells need no host half, since the PTY rides the
// web door.
//
// The phone is a node. Boot first asks the embedded Go node, through
// lib/node.dart and the platform shim into mobile/mobile.go, for its loopback
// origin: the phone's own durable store, with no network involved. On builds
// where the shim is not wired, the remote-server flow below is the fallback.
// The first launch asks for the server url, the server's own login page handles
// the password, and the webview's cookie store keeps it.

import 'dart:collection';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_inappwebview/flutter_inappwebview.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'bridge.dart';
import 'node.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  runApp(const GridwellApp());
}

class GridwellApp extends StatelessWidget {
  const GridwellApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Gridwell',
      theme: ThemeData.dark(useMaterial3: true),
      home: const _Root(),
    );
  }
}

/// _Root shows the server form until a URL is stored, then the client.
class _Root extends StatefulWidget {
  const _Root();

  @override
  State<_Root> createState() => _RootState();
}

class _RootState extends State<_Root> {
  String? _serverUrl;
  String? _localOrigin;
  bool _loaded = false;

  @override
  void initState() {
    super.initState();
    _boot();
  }

  Future<void> _boot() async {
    // The embedded node first, which is null when this build has no shim, then
    // the stored remote server. decideBoot owns the order.
    final local = await GwNode.start();
    final p = await SharedPreferences.getInstance();
    setState(() {
      _localOrigin = local;
      _serverUrl = p.getString('server_url');
      _loaded = true;
    });
  }

  Future<void> _setServer(String url) async {
    final p = await SharedPreferences.getInstance();
    await p.setString('server_url', url);
    setState(() => _serverUrl = url);
  }

  Future<void> _clearServer() async {
    final p = await SharedPreferences.getInstance();
    await p.remove('server_url');
    setState(() => _serverUrl = null);
  }

  @override
  Widget build(BuildContext context) {
    if (!_loaded) return const Scaffold(body: SizedBox.shrink());
    final target = decideBoot(_localOrigin, _serverUrl);
    if (target.origin == null) return ServerForm(onSubmit: _setServer);
    // On the local node there is no server to leave: the phone is the node, and
    // other machines are mounts from inside it.
    return GridwellScreen(
      origin: target.origin!,
      onLeaveServer: target.local ? null : _clearServer,
    );
  }
}

/// ServerForm asks for the server origin, such as a tailnet HTTPS address.
class ServerForm extends StatefulWidget {
  final Future<void> Function(String url) onSubmit;
  const ServerForm({super.key, required this.onSubmit});

  @override
  State<ServerForm> createState() => _ServerFormState();
}

class _ServerFormState extends State<ServerForm> {
  final _controller = TextEditingController();
  String? _error;

  void _submit() {
    final url = normalizeServerUrl(_controller.text);
    if (url == null) {
      setState(() => _error = 'enter the server address, e.g. https://host:10010');
      return;
    }
    widget.onSubmit(url);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 340),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const Text('gridwell', style: TextStyle(fontSize: 20)),
              const SizedBox(height: 16),
              TextField(
                controller: _controller,
                autofocus: true,
                keyboardType: TextInputType.url,
                autocorrect: false,
                decoration: InputDecoration(
                  labelText: 'server',
                  hintText: 'https://your-host:10010',
                  errorText: _error,
                ),
                onSubmitted: (_) => _submit(),
              ),
              const SizedBox(height: 16),
              FilledButton(onPressed: _submit, child: const Text('connect')),
            ],
          ),
        ),
      ),
    );
  }
}

/// normalizeServerUrl validates and canonicalizes the typed address: the scheme
/// defaults to https and a trailing slash is dropped. Null means not usable.
String? normalizeServerUrl(String input) {
  var s = input.trim();
  if (s.isEmpty) return null;
  if (!s.contains('://')) s = 'https://$s';
  final u = Uri.tryParse(s);
  if (u == null || u.host.isEmpty || (u.scheme != 'http' && u.scheme != 'https')) {
    return null;
  }
  return u.hasPort
      ? '${u.scheme}://${u.host}:${u.port}'
      : '${u.scheme}://${u.host}';
}

/// GridwellScreen is the running client: the host webview underneath,
/// live url views stacked over it at their pane bounds.
class GridwellScreen extends StatefulWidget {
  final String origin;
  /// onLeaveServer clears the stored remote server: the change-server
  /// affordance. It is null on the embedded local node, where the phone is the
  /// node and there is no server to leave.
  final Future<void> Function()? onLeaveServer;
  const GridwellScreen({super.key, required this.origin, required this.onLeaveServer});

  @override
  State<GridwellScreen> createState() => _GridwellScreenState();
}

class _GridwellScreenState extends State<GridwellScreen> implements ViewHost {
  InAppWebViewController? _hostController;
  final Map<String, InAppWebViewController> _liveControllers = {};
  late final BridgeState _bridge;

  @override
  void initState() {
    super.initState();
    _bridge = BridgeState(host: this, onChanged: () => setState(() {}));
  }

  // ── ViewHost: capture + navigation against the real controllers ──────────

  @override
  Future<FreezeCapture> capture(String paneId) async {
    final c = _liveControllers[paneId];
    if (c == null) return const FreezeCapture();
    try {
      final png = await c.takeScreenshot(
        screenshotConfiguration: ScreenshotConfiguration(
          compressFormat: CompressFormat.JPEG,
          quality: 80,
        ),
      );
      final url = (await c.getUrl())?.toString() ?? '';
      final title = await c.getTitle() ?? '';
      return FreezeCapture(
        jpegBase64: png == null ? '' : base64Encode(png),
        url: url,
        title: title,
      );
    } catch (_) {
      return const FreezeCapture(); // degrade: no capture, but the tile freezes
    }
  }

  @override
  Future<void> goBack(String paneId) async {
    await _liveControllers[paneId]?.goBack();
  }

  // ── wasm-bound callbacks through the host page ────────────────────────────

  void _fire(String name, Map<String, Object?> event) {
    _hostController?.evaluateJavascript(source: dispatchJS(name, jsonEncode(event)));
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: SafeArea(
        child: Stack(
          children: [
            Positioned.fill(
              child: InAppWebView(
                initialUrlRequest: URLRequest(url: WebUri('${widget.origin}/')),
                initialUserScripts: UnmodifiableListView([
                  UserScript(
                    source: gridwellUserScript,
                    injectionTime: UserScriptInjectionTime.AT_DOCUMENT_START,
                  ),
                ]),
                initialSettings: InAppWebViewSettings(
                  javaScriptEnabled: true,
                  transparentBackground: true,
                ),
                onWebViewCreated: (c) {
                  _hostController = c;
                  c.addJavaScriptHandler(
                    handlerName: 'gridwell',
                    callback: (args) async {
                      final method = args.isNotEmpty ? args[0] as String : '';
                      final callArgs =
                          args.length > 1 && args[1] is Map ? args[1] as Map : <dynamic, dynamic>{};
                      return await _bridge.handleCall(method, callArgs);
                    },
                  );
                },
                onReceivedError: (c, req, err) {
                  // The failure the user must see is the host page failing, on
                  // a wrong server url. Live-view errors flow to the wasm error
                  // surface instead.
                  if (req.isForMainFrame ?? false) {
                    _showServerError(err.description);
                  }
                },
              ),
            ),
            for (final v in _bridge.views)
              Positioned(
                left: v.bounds.x,
                top: v.bounds.y,
                width: v.bounds.width,
                height: v.bounds.height,
                child: Offstage(
                  offstage: v.hidden || v.bounds.width <= 0 || v.bounds.height <= 0,
                  child: Listener(
                    // A tap inside a live view must still transfer pane focus
                    // in the canvas beneath, since the native view swallows the
                    // pointer. That is the onLeftForward contract.
                    onPointerDown: (ev) {
                      final local = ev.position;
                      _fire('onLeftForward', {'x': local.dx, 'y': local.dy});
                    },
                    child: InAppWebView(
                      key: ValueKey('live-${v.paneId}'),
                      initialUrlRequest: URLRequest(url: WebUri(v.url)),
                      initialSettings: InAppWebViewSettings(javaScriptEnabled: true),
                      onWebViewCreated: (c) => _liveControllers[v.paneId] = c,
                      onCloseWindow: (c) => _liveControllers.remove(v.paneId),
                      onUpdateVisitedHistory: (c, url, _) {
                        if (url != null) {
                          _fire('onNav', {'tileId': v.tileId, 'url': url.toString()});
                        }
                      },
                    ),
                  ),
                ),
              ),
          ],
        ),
      ),
    );
  }

  void _showServerError(String description) {
    if (!mounted) return;
    final leave = widget.onLeaveServer;
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(
      content: Text('server unreachable: $description'),
      // The local node has no server to change, so a load failure there is its
      // own bug and the message stands alone.
      action: leave == null
          ? null
          : SnackBarAction(label: 'change server', onPressed: () => leave()),
      duration: const Duration(seconds: 10),
    ));
  }
}
