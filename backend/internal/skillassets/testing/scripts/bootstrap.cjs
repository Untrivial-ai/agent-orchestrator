// This wrapper stays in ignored build output, never in AO frontend source.
const { app } = require('electron');
const fs = require('node:fs');
const { randomBytes } = require('node:crypto');

if (app.isPackaged) throw new Error('Testing target must be unpackaged');
if (process.env.AO_TARGET_MARKER_ORACLE) {
  const marker = `SCREEN-${randomBytes(8).toString('hex').toUpperCase()}`;
  fs.writeFileSync(process.env.AO_TARGET_MARKER_ORACLE, JSON.stringify({ marker }), { mode: 0o600 });
  app.on('web-contents-created', (_event, contents) => {
    contents.on('did-finish-load', () => {
      if (!contents.getURL().startsWith('app://renderer/')) return;
      const source = `(() => {
        if (document.getElementById('ao-testing-marker')) return;
        const badge = document.createElement('div');
        badge.id = 'ao-testing-marker';
        badge.textContent = ${JSON.stringify(marker)};
        badge.style.cssText = 'position:fixed;right:20px;bottom:20px;z-index:2147483647;background:#111;color:#fff;padding:12px 16px;border:2px solid #fff;font:20px monospace;pointer-events:none';
        document.body.appendChild(badge);
      })()`;
      void contents.executeJavaScript(source).then(() => {
        console.log('Testing target visual fixture rendered');
      }).catch(() => {
        // Never log the marker or injected source, including on failure.
        console.error('Testing target visual fixture could not be rendered');
      });
    });
  });
}
require('./ao-main.cjs');
