// The embedded local node: every device is a node. The Go side is
// mobile/mobile.go, a full Gridwell node bound through gomobile, started with
// the app's private directory and answering on a loopback origin. This file is
// the Dart half of that seam: one MethodChannel call to the platform shim, in
// Kotlin or Swift, plus the pure boot decision, so it unit-tests without a
// device.
//
// Where the native shim is not wired, start() answers null on a
// MissingPluginException and the app falls back to the remote-server flow
// unchanged.

import 'package:flutter/services.dart';

/// The platform channel the native shim registers. Contract:
///   invokeMethod('start', {'home': "[app documents dir]/gridwell"})
///     → the loopback origin string (mobile.Start)
///   invokeMethod('stop') → null (mobile.Stop)
const nodeChannelName = 'gridwell/node';

class GwNode {
  static const MethodChannel channel = MethodChannel(nodeChannelName);

  /// start brings the embedded node up and returns its loopback origin, or null
  /// when no native shim is present, meaning this build has no bound Go node, or
  /// the node failed to start. Either way the caller falls back; the platform
  /// side logs which it was.
  static Future<String?> start() async {
    try {
      return await channel.invokeMethod<String>('start');
    } on MissingPluginException {
      return null;
    } on PlatformException {
      return null;
    }
  }

  /// stop shuts the embedded node down at app teardown. It is safe when no shim
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

  /// True when origin is the embedded local node, holding the phone's own data,
  /// where the leave-server affordance makes no sense.
  final bool local;

  const BootTarget._(this.origin, this.local);
}

/// decideBoot picks where the app lands. It is pure, and table-tested:
///   - a running local node always wins: the phone is a node, its own tiles are
///     the home, and other machines are mounts from in there;
///   - with no local node, because the shim is not wired on this build, a saved
///     remote server is used;
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
