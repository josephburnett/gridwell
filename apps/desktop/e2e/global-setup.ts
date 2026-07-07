import { sweepLeakedHomes } from './homes';

// Runs once before the suite: clean up whatever previous ABORTED runs left
// behind (issue #108). See homes.ts sweepLeakedHomes.
export default function globalSetup(): void {
  sweepLeakedHomes();
}
