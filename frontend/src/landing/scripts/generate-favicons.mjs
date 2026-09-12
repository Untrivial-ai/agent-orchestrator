// Keep the browser ICO and search PNG consistent with the SVG master.
// Run npm run icons:generate after editing public/favicon.svg.
// CI uses icons:check to catch stale generated assets without modifying them.
import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import sharp from "sharp";

const source = fileURLToPath(new URL("../public/favicon.svg", import.meta.url));
const check = process.argv.includes("--check");
async function output(relativePath, bytes) {
  const target = new URL(relativePath, import.meta.url);
  if (check) {
    const committed = await readFile(target);
    if (!committed.equals(bytes)) {
      throw new Error(`${relativePath} is stale; run npm run icons:generate`);
    }
  } else {
    await writeFile(target, bytes);
  }
}
await output("../public/favicon-192.png", await sharp(source).resize(192, 192).png().toBuffer());

const sizes = [16, 32, 48, 256];
const images = await Promise.all(
  sizes.map((size) => sharp(source).resize(size, size).png().toBuffer()),
);
const directory = Buffer.alloc(6 + sizes.length * 16);
directory.writeUInt16LE(1, 2); // ICO type
directory.writeUInt16LE(sizes.length, 4);
let offset = directory.length;
for (let index = 0; index < sizes.length; index++) {
  const entry = 6 + index * 16;
  directory[entry] = sizes[index] % 256; // Zero represents 256 pixels.
  directory[entry + 1] = sizes[index] % 256;
  directory.writeUInt16LE(1, entry + 4);
  directory.writeUInt16LE(32, entry + 6);
  directory.writeUInt32LE(images[index].length, entry + 8);
  directory.writeUInt32LE(offset, entry + 12);
  offset += images[index].length;
}
await output(
  "../src/app/favicon.ico",
  Buffer.concat([directory, ...images]),
);
