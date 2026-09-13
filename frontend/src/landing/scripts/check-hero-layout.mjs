// Run after `npm run build` in the landing workspace:
// node --test scripts/check-hero-layout.mjs
// Uses the frontend workspace's existing Playwright dependency/browsers.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { after, before, test } from "node:test";
import { chromium, webkit } from "playwright";

const output = fileURLToPath(new URL("../out/", import.meta.url));
const browserType = process.env.LANDING_TEST_BROWSER === "webkit" ? webkit : chromium;
let browser;
let server;
let origin;
const mime = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".woff2": "font/woff2" };

before(async () => {
  await readFile(path.join(output, "index.html"));
  server = createServer(async (req, res) => {
    const pathname = new URL(req.url, "http://localhost").pathname;
    const filename = path.resolve(output, `.${pathname.endsWith("/") ? `${pathname}index.html` : pathname}`);
    if (!filename.startsWith(output)) { res.writeHead(403).end(); return; }
    try {
      const body = await readFile(filename);
      res.setHeader("Content-Type", mime[path.extname(filename)] ?? "application/octet-stream");
      res.end(body);
    } catch { res.writeHead(404).end(); }
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${server.address().port}`;
  browser = await browserType.launch({
    headless: true,
    executablePath: process.env.LANDING_TEST_EXECUTABLE || undefined,
  });
});
after(async () => {
  await browser?.close();
  await new Promise(resolve => server ? server.close(resolve) : resolve());
});

async function newPage(options) {
  const context = await browser.newContext(options);
  // The export is the fixture: never contact GitHub, analytics, or other hosts.
  await context.route("**/*", route => route.request().url().startsWith(`${origin}/`) ? route.continue() : route.abort());
  return { context, page: await context.newPage() };
}

async function geometry(page) {
  return page.locator(".hero-mockup-window").evaluate(window => {
    const frame = window.closest(".hero-mockup-viewport").parentElement.getBoundingClientRect();
    const rect = window.getBoundingClientRect();
    return {
      frame: { x: frame.x, y: frame.y, width: frame.width, height: frame.height },
      window: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
      visible: getComputedStyle(window).visibility,
      layoutWidth: window.offsetWidth,
    };
  });
}
function assertFits({ frame, window, visible, layoutWidth }) {
  assert.equal(visible, "visible");
  assert.equal(layoutWidth, 1140, "board layout must not reflow on mobile");
  const scale = Math.min(1, (frame.width - 8) / 1140, (frame.height - 8) / 615);
  for (const [actual, expected] of [
    [window.width, 1140 * scale], [window.height, 615 * scale],
    [window.x, frame.x + (frame.width - window.width) / 2],
    [window.y, frame.y + (frame.height - window.height) / 2],
  ]) assert.ok(Math.abs(actual - expected) < 1, `${actual} should be within 1px of ${expected}`);
}

for (const width of [320, 390, 768, 1440]) {
  test(`visible and correctly fitted without JavaScript at ${width}px`, async () => {
    const { context, page } = await newPage({ javaScriptEnabled: false, viewport: { width, height: 900 } });
    try {
      await page.goto(origin, { waitUntil: "load" });
      assertFits(await geometry(page));
    } finally { await context.close(); }
  });
}

test("delayed hydration preserves geometry; resize and height constraints still fit", { timeout: 60000 }, async () => {
  const { context, page } = await newPage({ viewport: { width: 390, height: 844 } });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  await page.route("**/*.js", async route => {
    if (!route.request().url().startsWith(`${origin}/`)) { await route.abort(); return; }
    await gate;
    await route.continue();
  });
  try {
    await page.goto(origin, { waitUntil: "commit" });
    await page.locator(".hero-mockup-window").waitFor({ state: "visible" });
    await page.evaluate(() => Promise.all([400, 500, 600, 700].map(weight =>
      document.fonts.load(`${weight} 16px ${getComputedStyle(document.body).fontFamily}`))));
    const first = await geometry(page);
    assertFits(first);
    const initialText = await page.locator(".hero-mockup-window").textContent();
    release();
    await page.waitForLoadState("load");
    // An actual board update proves the component has hydrated and its timers run.
    await page.waitForFunction(text => document.querySelector(".hero-mockup-window").textContent !== text, initialText);
    const hydrated = await geometry(page);
    assertFits(hydrated);
    for (const key of ["x", "y", "width", "height"]) {
      assert.ok(Math.abs(first.window[key] - hydrated.window[key]) < 1, `hydration changed ${key}`);
    }
    await page.setViewportSize({ width: 844, height: 390 });
    assertFits(await geometry(page));
    await page.locator(".hero-mockup-viewport").evaluate(frame => {
      frame.parentElement.style.cssText += ";height:160px;min-height:0;aspect-ratio:auto";
    });
    assertFits(await geometry(page));
    assert.deepEqual(errors, []);
  } finally { release(); await context.close(); }
});

// Wide/tall frames must never upscale the board past its design size.
test("large frames retain the design size without JavaScript", async () => {
  const { context, page } = await newPage({ javaScriptEnabled: false, viewport: { width: 1600, height: 1200 } });
  try {
    await page.goto(origin, { waitUntil: "load" });
    await page.locator(".hero-mockup-viewport").evaluate(frame => {
      frame.parentElement.style.cssText += ";width:1400px;height:1000px;min-height:0";
    });
    const result = await geometry(page);
    assertFits(result);
    assert.equal(result.window.width, 1140);
    assert.equal(result.window.height, 615);
  } finally { await context.close(); }
});

test("legacy fallback measures before revealing and updates after resize", async () => {
  const { context, page } = await newPage({ viewport: { width: 390, height: 844 } });
  await context.addInitScript(() => {
    delete CSS.registerProperty;
  });
  // Emulate an engine that ignores @property; plain custom properties still work.
  await page.route("**/*.css", async route => {
    const response = await route.fetch();
    const body = (await response.text()).replace(/@property --mockup-[^{]+\{[^}]*\}/g, "");
    await route.fulfill({ response, body });
  });
  try {
    await page.goto(origin, { waitUntil: "load" });
    await page.waitForFunction(() => document.querySelector(".hero-mockup-window")?.style.visibility === "visible");
    assertFits(await geometry(page));
    await page.setViewportSize({ width: 844, height: 390 });
    await page.waitForFunction(() => {
      const el = document.querySelector(".hero-mockup-window");
      const frame = el.parentElement.getBoundingClientRect();
      const expected = Math.min(1, frame.width / 1140, frame.height / 615);
      return Math.abs(el.getBoundingClientRect().width - expected * 1140) < 1;
    });
    assertFits(await geometry(page));
  } finally { await context.close(); }
});
