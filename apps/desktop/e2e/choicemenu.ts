import { ElectronApplication } from '@playwright/test';
import { expect } from './fixtures';

// The native popup blocks under xvfb, so Menu.popup is intercepted and the
// menu stashed; the item's own click still settles main's promise. Same trick
// as e2e/theme.spec.ts.
export async function pickFromNativeMenu(electronApp: ElectronApplication, label: string): Promise<void> {
  await electronApp.evaluate(({ Menu }) => {
    const g = globalThis as any;
    g.__gwChoiceOrigPopup = Menu.prototype.popup;
    g.__gwChoiceMenu = null;
    (Menu.prototype as any).popup = function (this: any) {
      g.__gwChoiceMenu = this;
      return undefined;
    };
  });
  try {
    await expect
      .poll(() => electronApp.evaluate(() => Boolean((globalThis as any).__gwChoiceMenu)), {
        timeout: 10_000,
      })
      .toBe(true);
    await electronApp.evaluate((_: unknown, wanted: string) => {
      const m = (globalThis as any).__gwChoiceMenu;
      const row = m.items.find((i: any) => i.label === wanted);
      if (!row) throw new Error(`no ${wanted} row; got ${m.items.map((i: any) => i.label).join(', ')}`);
      row.click();
    }, label);
  } finally {
    await electronApp.evaluate(({ Menu }) => {
      const g = globalThis as any;
      if (g.__gwChoiceOrigPopup) (Menu.prototype as any).popup = g.__gwChoiceOrigPopup;
      delete g.__gwChoiceOrigPopup;
      delete g.__gwChoiceMenu;
    });
  }
}
