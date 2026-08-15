// The embedded LOCAL NODE (offline-plan phase 2: every device is a node).
// The Go side is mobile/mobile.go — a full Gridwell node bound via
// gomobile, started with the app's private directory and answering on a
// loopback origin. This file is the Dart half of that seam: one
// MethodChannel call to the platform shim (Kotlin/Swift, the
// real-hardware packaging pass), plus the PURE boot decision so it
// unit-tests without a device.
//
// Until the native shim is wired on a platform, start() answers null
// (MissingPluginException) and the app falls back to the remote-server
// flow unchanged — the local node arrives without breaking any build.

import 'package:flutter/services.dart';

/// The platform channel the native shim registers. Contract:
///   invokeMethod('start', {'home': "[app documents dir]/gridwell"})
///     → the loopback origin string (mobile.Start)
///   invokeMethod('stop') → null (mobile.Stop)
const nodeChannelName = 'gridwell/node';

class GwNode {
  static const MethodChannel channel = MethodChannel(nodeChannelName);

  /// start brings the embedded node up and returns its loopback origin,
  /// or null when no native shim is present (this build has no bound Go
  /// node) or the node failed to start — either way the caller falls
  /// back; the distinction is logged by the platform side.
  static Future<String?> start() async {
    try {
      return await channel.invokeMethod<String>('start');
    } on MissingPluginException {
      return null;
    } on PlatformException {
      return null;
    }
  }

  /// stop shuts the embedded node down (app teardown). Safe when no shim
  /// is present.
  static Future<void> stop() async {
    try {
      await channel.invokeMethod<void>('stop');
    } on MissingPluginException {
      // no shim, nothing running
    } on PlatformException {
      // already down
    }
  }
}

/// BootTarget is the pure boot decision, decided once at launch.
class BootTarget {
  /// The origin the host webview loads, or null when the server form must
  /// be shown first.
  final String? origin;

  /// True when origin is the embedded local node (the phone's own data) —
  /// the "leave server" affordance makes no sense there.
  final bool local;

  const BootTarget._(this.origin, this.local);
}

/// decideBoot picks where the app lands (pure — table-tested):
///   - a running LOCAL NODE always wins: the phone is a node, its own
///     tiles are the home, and other machines are mounts from in there;
///   - with no local node (shim not wired on this build), a SAVED remote
///     server keeps the pre-node behavior;
///   - with neither, the server form.
BootTarget decideBoot(String? localOrigin, String? savedServerUrl) {
  if (localOrigin != null && localOrigin.isNotEmpty) {
    return BootTarget._(localOrigin, true);
  }
  if (savedServerUrl != null && savedServerUrl.isNotEmpty) {
    return BootTarget._(savedServerUrl, false);
  }
  return const BootTarget._(null, false);
}
