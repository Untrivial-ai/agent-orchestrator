// Dependency-free test of the visual-only fixture. No Electron process starts.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { EventEmitter } = require('node:events');

for (const key of Object.keys(process.env)) {
  if (key.startsWith('AO_')) delete process.env[key];
}
const base = path.join(require('node:os').homedir(), '.ao/dev/agentic-target');
fs.mkdirSync(base, { recursive: true, mode: 0o700 });
const root = fs.mkdtempSync(path.join(base, 'fixture-test-'));
const oracle = path.join(root, 'oracle.json');
const app = new EventEmitter();
app.isPackaged = false;
const logs = [];
const script = fs.readFileSync(path.join(__dirname, '../../../skillassets/testing/scripts/bootstrap.cjs'), 'utf8');
try {
  vm.runInNewContext(script, {
    require(name) {
      if (name === 'electron') return { app };
      if (name === './ao-main.cjs') return {};
      return require(name);
    },
    process: { env: { AO_TARGET_MARKER_ORACLE: oracle } },
    console: { log: (line) => logs.push(line), error: (line) => logs.push(line) },
  });
  const marker = JSON.parse(fs.readFileSync(oracle, 'utf8')).marker;
  assert.match(marker, /^SCREEN-[0-9A-F]{16}$/);
  const foreign = new EventEmitter();
  foreign.getURL = () => 'https://example.test/';
  foreign.executeJavaScript = () => { throw new Error('foreign renderer modified'); };
  app.emit('web-contents-created', {}, foreign);
  foreign.emit('did-finish-load');
  const contents = new EventEmitter();
  contents.getURL = () => 'app://renderer/index.html';
  const children = [];
  contents.executeJavaScript = (source) => {
    vm.runInNewContext(source, { document: {
      getElementById: (id) => children.find((child) => child.id === id),
      createElement: () => ({ style: {} }),
      body: { appendChild: (child) => children.push(child) },
    } });
    return Promise.resolve();
  };
  app.emit('web-contents-created', {}, contents);
  contents.emit('did-finish-load');
  contents.emit('did-finish-load');
  assert.equal(children.length, 1);
  assert.equal(children[0].textContent, marker);
  assert.ok(children[0].style.cssText.includes('pointer-events:none'));
  setImmediate(() => {
    assert.ok(logs.length > 0);
    assert.ok(logs.every((line) => !line.includes(marker)));
    console.log('Visual fixture test passed');
    fs.rmSync(root, { recursive: true });
  });
} catch (error) {
  fs.rmSync(root, { recursive: true });
  throw error;
}
