import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import path from 'node:path';
import { buildRepairInstaller, distribution, renderGuard, validateMetadata } from './build-mac-repair-installer.mjs';

const version = '0.12.12-nightly.202609061223';
const metadata = { id: 'dev.agent-orchestrator.desktop', version, build: version, executable: 'agent-orchestrator', archs: 'arm64' };
async function fixture(t) {
  const base = path.join(process.env.AO_DATA_DIR || path.join(homedir(), '.ao'), 'repair-installer-tests');
  await mkdir(base, { recursive: true });
  const dir = await mkdtemp(path.join(base, 'case-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  return dir;
}
const native = (command, args) => {
  const result = spawnSync(command, args, { encoding: 'utf8' });
  if (result.error || result.status !== 0) throw new Error(`${command}: ${result.error?.message || result.stderr}`);
  return result.stdout.trim();
};
test('accepts supported stable/nightly versions and both executable architectures', () => {
  assert.deepEqual(validateMetadata(metadata).architectures, ['arm64']);
  assert.deepEqual(validateMetadata({ ...metadata, version: '1.2.3', build: '1.2.3', archs: 'x86_64 arm64' }).architectures, ['x86_64', 'arm64']);
  for (const bad of [{ id: 'unrelated.app' }, { version: '1.2.3-beta.1' }, { build: '123' }, { executable: '../evil' }, { archs: '' }, { archs: 'arm64 arm64' }, { archs: 'i386' }]) assert.throws(() => validateMetadata({ ...metadata, ...bad }));
});
test('Installer GUI delegates to the guard and limits target volume and location', () => {
  const xml = distribution(version, ['arm64']);
  assert.match(xml, /system.run\('guard.sh', '', '\/Applications', '\/'\) === 0/);
  assert.match(xml, /my.target.mountpoint === '\/'/);
  assert.match(xml, /enable_anywhere="false"/);
  assert.match(xml, /my.result.type = 'Fatal'/);
  assert.match(xml, /<must-close><app id="dev.agent-orchestrator.desktop"\/>/);
});
async function guardFixture(t) {
  const dir = await fixture(t), app = path.join(dir, 'Applications/Agent Orchestrator.app');
  await mkdir(path.join(app, 'Contents'), { recursive: true });
  const files = { ps: '', arch: '1', uname: 'x86_64', id: '0', bundle: metadata.id, old: version, build: version };
  for (const [key, value] of Object.entries(files)) await writeFile(path.join(dir, key), value);
  const helpers = {};
  for (const key of ['ps', 'arch', 'uname', 'id']) {
    helpers[key] = path.join(dir, `get-${key}`);
    await writeFile(helpers[key], `#!/bin/bash\n/bin/cat '${path.join(dir, key)}'\n`, { mode: 0o755 });
  }
  helpers.plist = path.join(dir, 'plist');
  await writeFile(helpers.plist, `#!/bin/bash\ncase "$3" in /Applications/Other.app/*) echo foreign.app; exit 0;; esac\ncase "$2" in *CFBundleIdentifier) /bin/cat '${dir}/bundle';; *CFBundleShortVersionString) /bin/cat '${dir}/old';; *CFBundleVersion) /bin/cat '${dir}/build';; *) exit 1;; esac\n`, { mode: 0o755 });
  let guard = await renderGuard({ version, architectures: ['arm64'] }, true);
  // Test-only source transformation: the shipped script has no environment override.
  for (const [from, to] of Object.entries({ '/usr/sbin/sysctl': helpers.arch, '/usr/bin/uname': helpers.uname, '/usr/bin/id': helpers.id, '/bin/ps': helpers.ps, '/usr/libexec/PlistBuddy': helpers.plist })) guard = guard.replaceAll(from, `'${to}'`);
  guard = guard.replaceAll('/Applications', `${dir}/Applications`);
  const file = path.join(dir, 'preinstall');
  await writeFile(file, guard);
  return { dir, app, set: (key, value) => writeFile(path.join(dir, key), value), check: (volume = '/') => spawnSync('/bin/bash', [file, '', '/Applications', volume], { encoding: 'utf8' }) };
}
test('root preinstall permits exact repair, earlier nightly and stable versions; rejects downgrades/unknown builds', async (t) => {
  const f = await guardFixture(t);
  assert.equal(f.check().status, 0);
  for (const old of ['0.12.11', '0.12.12-nightly.202609051223']) {
    await f.set('old', old); await f.set('build', old); assert.equal(f.check().status, 0);
  }
  for (const old of ['0.12.13', '0.12.12', '0.12.12-nightly.202609071223', '0.12.12-beta.1', '0.12.12-nightly.1', '0.12.12-nightly.2026090512230', '00.12.11', '0.012.11', '0.12.011', 'garbage']) {
    await f.set('old', old); await f.set('build', old); assert.notEqual(f.check().status, 0, old);
  }
  await f.set('old', version); await f.set('build', '123'); assert.notEqual(f.check().status, 0);
});
test('root preinstall refuses nonroot, foreign volume, unrelated bundles, symlinks and wrong architecture', async (t) => {
  const f = await guardFixture(t);
  assert.notEqual(f.check('/Volumes/Other').status, 0);
  for (const [key, invalid, valid] of [['id', '501', '0'], ['bundle', 'foreign.app', metadata.id], ['arch', '0', '1']]) {
    await f.set(key, invalid); assert.notEqual(f.check().status, 0, key); await f.set(key, valid);
  }
  await rm(f.app, { recursive: true }); await symlink('/nonexistent', f.app);
  assert.notEqual(f.check().status, 0);
});
test('running executable and updater checks reject AO narrowly without matching argument text or other apps', async (t) => {
  const f = await guardFixture(t);
  for (const process of ['/Applications/Renamed.app/Contents/Frameworks/Squirrel.framework/Versions/A/Resources/ShipIt', '/Applications/Renamed.app/Contents/MacOS/agent-orchestrator', '/Applications/Agent Orchestrator.app/Contents/MacOS/agent-orchestrator', '/private/var/folders/x/Agent Orchestrator.app/Contents/Frameworks/Agent Orchestrator Helper.app/Contents/MacOS/helper', '/Users/test/Library/Caches/dev.agent-orchestrator.desktop.ShipIt/update/ShipIt']) {
    await f.set('ps', process); assert.notEqual(f.check().status, 0, process);
  }
  await f.set('ps', '/Applications/Other.app/Contents/MacOS/ShipIt\n/bin/bash\n/usr/bin/node'); assert.equal(f.check().status, 0);
});
async function buildFixture(t, failAt) {
  const dir = await fixture(t), app = path.join(dir, 'Agent Orchestrator.app');
  await mkdir(app);
  const calls = [];
  const execute = async (command, args) => {
    calls.push([command, args]);
    if (command === failAt) throw new Error('injected command failure');
    if (command.endsWith('PlistBuddy')) return ({ CFBundleIdentifier: metadata.id, CFBundleShortVersionString: version, CFBundleVersion: version, CFBundleExecutable: metadata.executable })[args[1].split(':')[1]];
    if (command.endsWith('lipo')) return 'arm64';
    if (command.endsWith('productbuild')) await writeFile(args.at(-1), 'fixture package');
    if (args[0] === 'notarytool') return '{"status":"Accepted"}';
    return '';
  };
  return { dir, app, calls, execute, options: { app, output: path.join(dir, 'repair.pkg'), dataDir: dir, identity: 'Developer ID Installer: Example (TEAMID)', keychainProfile: 'profile' } };
}
test('signs, notarizes, staples and canonically verifies before emitting output; never overwrites', async (t) => {
  const f = await buildFixture(t);
  assert.equal(await buildRepairInstaller(f.options, f.execute), f.options.output);
  const product = f.calls.find(([c]) => c.endsWith('productbuild'));
  assert.ok(product[1].includes('--sign'));
  const notarization = f.calls.find(([, a]) => a[0] === 'notarytool')[1];
  assert.equal(notarization[notarization.indexOf('--timeout') + 1], '30m');
  assert.ok(f.calls.some(([, a]) => a[0] === '--expand-full'));
  assert.ok(f.calls.some(([c, a]) => c.endsWith('bash') && a[1].includes('/Payload/Agent Orchestrator.app')));
  assert.deepEqual(f.calls.slice(-3).map(([, a]) => a[0] === 'notarytool' ? 'notarytool' : a[0] === 'stapler' ? 'stapler' : 'verify'), ['notarytool', 'stapler', 'verify']);
  await assert.rejects(buildRepairInstaller(f.options, f.execute), /already exists/);
  assert.equal(await readFile(f.options.output, 'utf8'), 'fixture package');
});
test('credentials are mandatory; unsigned output is conspicuous and command failure leaves no package', async (t) => {
  const f = await buildFixture(t, '/usr/bin/productbuild');
  await assert.rejects(buildRepairInstaller({ ...f.options, identity: undefined }, f.execute), /identity/);
  await assert.rejects(buildRepairInstaller({ ...f.options, unsignedForTesting: true }, f.execute), /credentials/);
  await assert.rejects(buildRepairInstaller(f.options, f.execute), /injected command failure/);
  assert.deepEqual(await readdir(f.dir), ['Agent Orchestrator.app']);
  const g = await buildFixture(t);
  const options = { ...g.options, identity: undefined, keychainProfile: undefined, unsignedForTesting: true };
  await assert.rejects(buildRepairInstaller(options, g.execute), /unsigned.pkg/);
  options.output = path.join(g.dir, 'fixture.unsigned.pkg');
  await buildRepairInstaller(options, g.execute);
  assert.equal(g.calls.some(([, a]) => a[0] === 'notarytool'), false);
});
test('notarization rejection and canonical verifier failure fail closed', async (t) => {
  const f = await buildFixture(t);
  await assert.rejects(buildRepairInstaller(f.options, async (c, a) => a[0] === 'notarytool' ? '{"status":"Rejected"}' : f.execute(c, a)), /did not accept/);
  assert.deepEqual(await readdir(f.dir), ['Agent Orchestrator.app']);
  const g = await buildFixture(t, '/bin/bash');
  await assert.rejects(buildRepairInstaller(g.options, g.execute), /injected command failure/);
});
test('native macOS packaging produces a fixed atomic bundle replacement and executable guards without installing', { skip: process.platform !== 'darwin' }, async (t) => {
  const dir = await fixture(t), app = path.join(dir, 'Agent Orchestrator.app');
  await mkdir(path.join(app, 'Contents/MacOS'), { recursive: true });
  await writeFile(path.join(app, 'Contents/Info.plist'), `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>${metadata.id}</string><key>CFBundleShortVersionString</key><string>${version}</string><key>CFBundleVersion</key><string>${version}</string><key>CFBundleExecutable</key><string>agent-orchestrator</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>`);
  await writeFile(path.join(app, 'Contents/MacOS/agent-orchestrator'), '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  const output = path.join(dir, 'fixture.unsigned.pkg');
  await buildRepairInstaller({ app, output, unsignedForTesting: true, dataDir: dir }, (c, a) => {
    // Only this test fixture bypasses trust/lipo: it is a tiny non-release shell app.
    if (c === '/bin/bash') return '';
    if (c === '/usr/bin/lipo') return 'arm64';
    return native(c, a);
  });
  const expanded = path.join(dir, 'inspect');
  native('/usr/sbin/pkgutil', ['--expand-full', output, expanded]);
  const component = path.join(expanded, 'repair-component.pkg');
  const info = await readFile(path.join(component, 'PackageInfo'), 'utf8');
  assert.match(info, /install-location="\/Applications"/);
  assert.match(info, /<upgrade-bundle>/);
  assert.doesNotMatch(info, /<relocate>/);
  assert.equal(await readFile(path.join(component, 'Payload/Agent Orchestrator.app/Contents/MacOS/agent-orchestrator'), 'utf8'), '#!/bin/sh\nexit 0\n');
  assert.match(await readFile(path.join(component, 'Scripts/preinstall'), 'utf8'), /Preinstall requires root/);
});
