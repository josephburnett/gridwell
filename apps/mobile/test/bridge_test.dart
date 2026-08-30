// The mobile bridge's pure half under plain `flutter test`: the view registry
// lifecycle, the dispatch contract, and the injected JS. Everything that decides
// behavior, with no platform channel.

import 'package:flutter_test/flutter_test.dart';
import 'package:gridwell_mobile/bridge.dart';
import 'package:gridwell_mobile/main.dart' show normalizeServerUrl;

class FakeHost implements ViewHost {
  final captured = <String>[];
  final backs = <String>[];
  FreezeCapture next = const FreezeCapture(jpegBase64: 'anVwZw==', url: 'https://x/', title: 'X');

  @override
  Future<FreezeCapture> capture(String paneId) async {
    captured.add(paneId);
    return next;
  }

  @override
  Future<void> goBack(String paneId) async {
    backs.add(paneId);
  }
}

void main() {
  group('BridgeState', () {
    late FakeHost host;
    late BridgeState b;
    var changes = 0;

    setUp(() {
      host = FakeHost();
      changes = 0;
      b = BridgeState(host: host, onChanged: () => changes++);
    });

    test('place → bounds → hide → remove is the Electron lifecycle', () async {
      await b.handleCall('placeWebview', {
        'paneId': 'p1',
        'tileId': 'uf1/5',
        'url': 'https://example.com',
        'bounds': {'x': 10, 'y': 20, 'width': 300, 'height': 200},
      });
      expect(b.views.length, 1);
      expect(b.view('p1')!.url, 'https://example.com');
      expect(b.view('p1')!.bounds.width, 300);

      await b.handleCall('setBounds', {
        'paneId': 'p1',
        'bounds': {'x': 0, 'y': 0, 'width': 100, 'height': 100},
      });
      expect(b.view('p1')!.bounds.x, 0);

      await b.handleCall('setHidden', {'paneId': 'p1', 'hidden': true, 'focused': false});
      expect(b.view('p1')!.hidden, true);

      final res = await b.handleCall('removeWebview', {'paneId': 'p1'}) as Map;
      expect(res['jpegBase64'], 'anVwZw==');
      expect(res['url'], 'https://x/');
      expect(res['title'], 'X');
      expect(res['history'], '', reason: 'mobile sends no history; empty leaves the stored value untouched');
      expect(b.views, isEmpty);
      expect(host.captured, ['p1']);
      expect(changes, 4, reason: 'every mutation notifies the widget layer');
    });

    test('re-place on the same pane reuses: same url does not reset, new url renavigates', () async {
      await b.handleCall('placeWebview', {
        'paneId': 'p1',
        'tileId': 't',
        'url': 'https://a/',
        'bounds': {'x': 0, 'y': 0, 'width': 10, 'height': 10},
      });
      await b.handleCall('setHidden', {'paneId': 'p1', 'hidden': true});
      await b.handleCall('placeWebview', {
        'paneId': 'p1',
        'tileId': 't',
        'url': 'https://a/',
        'bounds': {'x': 5, 'y': 5, 'width': 20, 'height': 20},
      });
      expect(b.views.length, 1);
      expect(b.view('p1')!.hidden, false, reason: 'a keep-alive return unparks');
      expect(b.view('p1')!.bounds.width, 20);

      await b.handleCall('placeWebview', {
        'paneId': 'p1',
        'tileId': 't',
        'url': 'https://b/',
        'bounds': {'x': 5, 'y': 5, 'width': 20, 'height': 20},
      });
      expect(b.view('p1')!.url, 'https://b/');
    });

    test('removeWebview on an unknown pane answers an EMPTY capture, never throws', () async {
      final res = await b.handleCall('removeWebview', {'paneId': 'nope'}) as Map;
      expect(res['jpegBase64'], '');
      expect(host.captured, isEmpty);
    });

    test('goBack routes to the pane\'s controller', () async {
      await b.handleCall('goBack', {'paneId': 'p9'});
      expect(host.backs, ['p9']);
    });

    test('unknown and declared-away methods are inert (skew rule)', () async {
      expect(await b.handleCall('setZoom', {'paneId': 'p1', 'zoom': 2}), isNull);
      expect(await b.handleCall('showMenu', {'paneId': 'p1'}), isNull);
      expect(await b.handleCall('someFutureMethod', {}), isNull);
    });
  });

  group('gridwellUserScript', () {
    test('declares the mobile capability set: liveUrl only', () {
      expect(gridwellUserScript, contains('caps: { liveUrl: true }'));
    });

    test('defines EVERY onX registrar the wasm installs at boot', () {
      // installWebviewListeners in client/wasm/webview_bridge.go calls each of
      // these unconditionally; a missing one is a boot TypeError.
      for (final name in [
        'onFrame', 'onNav', 'onRightForward', 'onMiddleForward', 'onLeftForward',
        'onOpenBelow', 'onFreezeURL', 'onZoomKey', 'onError',
      ]) {
        expect(gridwellUserScript, contains('$name:'), reason: '$name registrar missing');
      }
    });

    test('defines every call method the wasm may invoke', () {
      for (final name in [
        'placeWebview', 'setBounds', 'setHidden', 'setZoom', 'goBack', 'showMenu',
        'removeWebview',
      ]) {
        expect(gridwellUserScript, contains('$name:'), reason: '$name method missing');
      }
    });
  });

  test('dispatchJS formats a __gwDispatch call', () {
    expect(dispatchJS('onNav', '{"url":"https://x/"}'),
        'window.__gwDispatch(\'onNav\', {"url":"https://x/"});');
  });

  group('normalizeServerUrl', () {
    test('defaults the scheme, keeps port, drops path', () {
      expect(normalizeServerUrl('rtb.example.ts.net:10010'), 'https://rtb.example.ts.net:10010');
      expect(normalizeServerUrl('http://192.168.1.4:8080'), 'http://192.168.1.4:8080');
      expect(normalizeServerUrl('https://host'), 'https://host');
      expect(normalizeServerUrl('  https://host:1/  '), 'https://host:1');
    });
    test('rejects garbage', () {
      expect(normalizeServerUrl(''), isNull);
      expect(normalizeServerUrl('   '), isNull);
      expect(normalizeServerUrl('ftp://x'), isNull);
    });
  });
}
