# A ledger of two rows

The shape docs/flake-ledger.md carries, held here so the script's verdict is
read against a fixed page rather than the live one.

| Spec | What flaked | Mechanism | Closed by |
|---|---|---|---|
| `apps/desktop/e2e/lost-release.spec.ts` | a press that read as lost | the harness had no fact "the tree this run tests" | the run copies its artifacts once |

## Open: no mechanism yet

| Spec | What flaked | Standing hypothesis | State |
|---|---|---|---|
| `apps/desktop/e2e/teardown-dirty.spec.ts` | an unattributed teardown error | none yet | OPEN |
