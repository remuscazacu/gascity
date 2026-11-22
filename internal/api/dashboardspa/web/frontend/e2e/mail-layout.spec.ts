import type { Page } from '@playwright/test';
import { CITY_BASE } from './fixtures/expected';
import { gotoCityRoute } from './support/renderGuards';
import { expect, test } from './support/fixtures';

// sr-u9ua: the Mail page must never scroll sideways, at any width, even with a
// very long subject, a very long sender and an unbroken token in the body. The
// seeded corpus has only short mail, so the spec adds one probe message to the
// real mailbox response (the rest of the response is the fake supervisor's own).

const LONG_SUBJECT = `Layout probe: ${'a very long subject line that keeps on going '.repeat(8).trim()}`;
const LONG_SENDER = `rig/support.intake-poller-overflow-seat-with-a-long-name-${'y'.repeat(40)}`;
const UNBROKEN = `handoff-${'7f3a9c0e2b4d4f61'.repeat(16)}.json`;
const PROBE_THREAD = 'layout-probe-thread';

async function injectProbeMail(page: Page): Promise<void> {
  // The mailbox list only (…/mail or …/mail?limit=…), not …/mail/thread/….
  await page.route(/\/v0\/city\/[^/]+\/mail(\?.*)?$/, async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const response = await route.fetch();
    const body = (await response.json()) as { items?: Array<Record<string, unknown>> };
    const items = Array.isArray(body.items) ? body.items : [];
    // Copy a seeded message addressed to the operator ('human' on the wire)
    // so the probe lands in the operator's inbox.
    const template = items.find((m) => m.to === 'human') ?? items[0] ?? {};
    const probe = {
      ...template,
      id: 'layout-probe',
      from: LONG_SENDER,
      subject: LONG_SUBJECT,
      body: `${UNBROKEN}\nsecond line`,
      created_at: new Date().toISOString(),
      read: false,
      thread_id: PROBE_THREAD,
    };
    await route.fulfill({ response, json: { ...body, items: [probe, ...items] } });
  });
  // No such thread on the fake supervisor: answer 404 so the page takes its
  // documented fallback (the thread's messages filtered out of the mailbox).
  await page.route(new RegExp(`/v0/city/[^/]+/mail/thread/${PROBE_THREAD}`), (route) =>
    route.fulfill({ status: 404, json: { status: 404, title: 'Not Found' } }),
  );
}

async function overflow(page: Page): Promise<{ page: number; boxes: number }> {
  return page.evaluate(() => {
    const root = document.scrollingElement ?? document.documentElement;
    const pageOverflow = root.scrollWidth - root.clientWidth;
    let boxOverflow = 0;
    for (const table of Array.from(document.querySelectorAll('table'))) {
      const box = table.parentElement;
      if (box) boxOverflow = Math.max(boxOverflow, box.scrollWidth - box.clientWidth);
    }
    return { page: pageOverflow, boxes: boxOverflow };
  });
}

for (const width of [390, 768, 1024, 1280]) {
  test(`mail never scrolls sideways at ${width}px (sr-u9ua)`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await injectProbeMail(page);
    await gotoCityRoute(page, CITY_BASE, '/mail');
    await expect(page.getByText(LONG_SUBJECT)).toBeAttached();

    expect(await overflow(page)).toEqual({ page: 0, boxes: 0 });

    const header = page.locator('table thead').first();
    const probeRow = page.locator('tr', { hasText: LONG_SUBJECT });
    if (width < 640) {
      // Phone: rows stack, the header row (and its sort buttons) is hidden,
      // and the sender gets the row's width minus the checkbox and the time.
      await expect(header).toBeHidden();
      const [row, from] = await Promise.all([
        probeRow.boundingBox(),
        probeRow.locator('td').nth(1).boundingBox(),
      ]);
      expect(from!.width).toBeGreaterThan(row!.width * 0.6);
    } else {
      // The checkbox and When columns are only as wide as their content
      // (a checkbox; "365d" plus the "When ↓" header).
      const ths = header.locator('th');
      expect((await ths.nth(0).boundingBox())!.width).toBeLessThanOrEqual(25);
      expect((await ths.nth(2).boundingBox())!.width).toBeLessThanOrEqual(61);
      const labels = (await header.locator('th').allInnerTexts())
        .map((t) => t.replace(/[↑↓]/g, '').trim())
        .filter((t) => t.length > 0);
      expect(labels.map((t) => t.toLowerCase())).toEqual(['from', 'when', 'subject']);
    }
  });
}

test('the thread view wraps an unbroken token at 390px (sr-u9ua)', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 900 });
  await injectProbeMail(page);
  await gotoCityRoute(page, CITY_BASE, '/mail');
  await page.getByText(LONG_SUBJECT).click();
  const body = page.locator('pre', { hasText: UNBROKEN });
  await expect(body).toBeVisible();

  const wraps = await body.evaluate((el) => el.scrollWidth <= el.clientWidth);
  expect(wraps).toBe(true);
  expect((await overflow(page)).page).toBe(0);
});
