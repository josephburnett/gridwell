import { test } from 'node:test';
import assert from 'node:assert/strict';
import { sidecarExitMessage } from './sidecar-messages';

// Regression guard for issue #46 point 1: a post-boot sidecar crash was
// completely unobserved (the exit listener that could report it only acted
// before the boot promise settled). The message must tell the user something
// actionable rather than nothing at all.
test('sidecarExitMessage reports a signal kill', () => {
  const msg = sidecarExitMessage(null, 'SIGKILL');
  assert.ok(msg.includes('SIGKILL'));
  assert.ok(msg.toLowerCase().includes('restart'));
});

test('sidecarExitMessage reports a nonzero exit code', () => {
  const msg = sidecarExitMessage(1, null);
  assert.ok(msg.includes('code 1'));
});

test('sidecarExitMessage tolerates neither code nor signal being known', () => {
  const msg = sidecarExitMessage(null, null);
  assert.ok(msg.includes('unknown'));
});
