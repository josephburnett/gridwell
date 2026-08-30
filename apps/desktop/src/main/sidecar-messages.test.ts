import { test } from 'node:test';
import assert from 'node:assert/strict';
import { sidecarExitMessage } from './sidecar-messages';

// A post-boot sidecar crash must reach the user, so the message has to name
// the cause and say what to do.
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
