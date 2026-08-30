// The Dart half of the embedded-node seam, lib/node.dart: the channel wrapper's
// degrade contract, where no shim means null and never a crash, plus the pure
// boot decision. The Go half is tested in mobile/mobile_test.go, and the
// platform shim itself needs real hardware.

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:gridwell_mobile/node.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  tearDown(() {
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(GwNode.channel, null);
  });

  test('start returns the origin the shim answers', () async {
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(GwNode.channel, (call) async {
      expect(call.method, 'start');
      return 'http://127.0.0.1:53712';
    });
    expect(await GwNode.start(), 'http://127.0.0.1:53712');
  });

  test('start degrades to null with no shim (MissingPluginException)', () async {
    // No handler installed at all: the shape of a build without the bound Go
    // node. The app must fall back, never crash.
    expect(await GwNode.start(), isNull);
  });

  test('start degrades to null when the shim fails', () async {
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(GwNode.channel, (call) async {
      throw PlatformException(code: 'node', message: 'listen failed');
    });
    expect(await GwNode.start(), isNull);
  });

  test('stop is safe with and without a shim', () async {
    await GwNode.stop(); // no shim installed
    var stopped = false;
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(GwNode.channel, (call) async {
      if (call.method == 'stop') stopped = true;
      return null;
    });
    await GwNode.stop();
    expect(stopped, isTrue);
  });

  group('decideBoot', () {
    test('the local node always wins', () {
      final t = decideBoot('http://127.0.0.1:1', 'https://home.example');
      expect(t.origin, 'http://127.0.0.1:1');
      expect(t.local, isTrue);
    });
    test('a saved server keeps the pre-node behavior without a shim', () {
      final t = decideBoot(null, 'https://home.example');
      expect(t.origin, 'https://home.example');
      expect(t.local, isFalse);
    });
    test('neither → the server form', () {
      final t = decideBoot(null, null);
      expect(t.origin, isNull);
      expect(t.local, isFalse);
    });
    test('empty strings count as absent', () {
      expect(decideBoot('', '').origin, isNull);
    });
  });
}
