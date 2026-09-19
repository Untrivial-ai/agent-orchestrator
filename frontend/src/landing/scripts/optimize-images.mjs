// Regenerates every raster asset the site actually ships from the full-size
// sources in assets/. Sources live outside public/ on purpose: `output: export`
// copies public/ verbatim into out/, so anything parked there is deployed even
// when no page references it (that is how 43 MB of unused PNG masters ended up
// on the CDN).
//
// Usage: npm run images:optimize
import { mkdir, readdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import sharp from "sharp";
import { optimize as svgoOptimize } from "svgo";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const src = (...p) => path.join(root, "assets", ...p);
const out = (...p) => path.join(root, "public", ...p);

// webp q72 / avif q50 are the lowest settings that survived a side-by-side
// check on these screenshots; effort 6 buys ~8% size for build time we only
// pay when regenerating.
const WEBP = { quality: 72, effort: 6 };
const AVIF = { quality: 50, effort: 6 };

let generated = 0;

async function emit(file, buffer) {
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, buffer);
  generated++;
  const rel = path.relative(root, file);
  process.stdout.write(`  ${rel.padEnd(58)} ${(buffer.length / 1024).toFixed(1)} KB\n`);
}

/**
 * Emits `<name>-<width>.<fmt>` for each width. `unsuffixed` additionally writes
 * `<name>.webp` at the largest width, which is what routes still referencing
 * the pre-srcset filenames load; the hero does not need one.
 */
async function responsive(source, target, widths, formats, { unsuffixed = true } = {}) {
  const input = sharp(source);
  const meta = await input.metadata();
  for (const width of widths) {
    if (width > meta.width) continue;
    for (const fmt of formats) {
      const buf = await sharp(source)
        .resize({ width, withoutEnlargement: true })
        [fmt](fmt === "avif" ? AVIF : WEBP)
        .toBuffer();
      await emit(out(`${target}-${width}.${fmt}`), buf);
    }
  }
  if (!unsuffixed) return;
  const largest = Math.min(Math.max(...widths), meta.width);
  const fallback = await sharp(source)
    .resize({ width: largest, withoutEnlargement: true })
    .webp(WEBP)
    .toBuffer();
  await emit(out(`${target}.webp`), fallback);
}

async function icon(source, target, size) {
  const buf = await sharp(source)
    .resize({ width: size, height: size, fit: "contain", background: { r: 0, g: 0, b: 0, alpha: 0 } })
    .png({ compressionLevel: 9, palette: true })
    .toBuffer();
  await emit(out(target), buf);
}

console.log("backgrounds");
// The hero is the LCP element and is served as a single-format srcset so the
// preload scanner and the element always agree on one candidate.
await responsive(src("images", "hero-background.jpeg"), "optimized/hero-background", [720, 1080, 1440], ["webp"], { unsuffixed: false });
for (const name of ["feature", "feature2", "feature3", "feature4"]) {
  await responsive(src("images", `${name}.png`), `optimized/${name}`, [640, 960, 1280], ["avif", "webp"]);
}
for (const file of await readdir(src("images", "design-partners"))) {
  const name = path.parse(file).name;
  await responsive(src("images", "design-partners", file), `optimized/design-partners/${name}`, [768, 1536], ["avif", "webp"]);
}

console.log("icons (rendered at 12-32 CSS px; 64 px covers 2x)");
await icon(src("icons", "aider.png"), "app-icons/agents/aider.png", 64);
await icon(src("icons", "aider.png"), "docs/logos/aider.png", 64);
await icon(src("icons", "agy.png"), "app-icons/agents/agy.png", 64);
await icon(src("icons", "muse.png"), "app-icons/agents/muse.png", 64);

console.log("vector");
const logoSource = await readFile(src("icons", "ao-logo.svg"), "utf8");
const logo = svgoOptimize(logoSource, {
  multipass: true,
  plugins: [{ name: "preset-default", params: { overrides: { cleanupNumericValues: { floatPrecision: 2 } } } }],
});
await emit(out("ao-logo.svg"), Buffer.from(logo.data));

console.log(`\n${generated} files written to public/`);
