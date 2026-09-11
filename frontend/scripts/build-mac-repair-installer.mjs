#!/usr/bin/env node
/** Build API: await buildRepairInstaller({ app, output, identity, keychainProfile,
 * unsignedForTesting: false, dataDir: process.env.AO_DATA_DIR }); returns output.
 * Input must be the exact signed/stapled release .app. Never installs or launches AO.
 * The optional second argument injects command execution for unit tests only.
 */
import { spawnSync } from 'node:child_process';
import { constants } from 'node:fs';
import { copyFile, lstat, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const verifier = path.join(here, 'verify-mac-artifact.sh');
const appName = 'Agent Orchestrator.app';
const bundleID = 'dev.agent-orchestrator.desktop';
const packageID = `${bundleID}.repair`;
const versionPattern = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-nightly\.(\d{12}))?$/;
const xml = (value) => String(value).replace(/[<>&"']/g, (c) => ({ '<': '&lt;', '>': '&gt;', '&': '&amp;', '"': '&quot;', "'": '&apos;' })[c]);
function run(command, args) {
  const result = spawnSync(command, args, { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 });
  if (result.error || result.status !== 0) throw new Error(`${command} failed: ${result.error?.message ?? result.stderr ?? result.status}`);
  return result.stdout.trim();
}
export function validateMetadata({ id, version, build, executable, archs }) {
  if (id !== bundleID) throw new Error(`Expected bundle identifier ${bundleID}`);
  if (!versionPattern.test(version) || version !== build) throw new Error('Expected matching supported version/build (stable or nightly)');
  if (!/^[A-Za-z0-9_-]+$/.test(executable)) throw new Error('Invalid bundle executable');
  const architectures = archs.trim().split(/\s+/);
  if (!architectures.length || architectures.some((a) => !['arm64', 'x86_64'].includes(a)) || new Set(architectures).size !== architectures.length) throw new Error('Unsupported executable architectures');
  return { version, executable, architectures };
}
export async function renderGuard({ version, architectures }, root = false) {
  if (!versionPattern.test(version) || architectures.length === 0 || architectures.some((a) => !['arm64', 'x86_64'].includes(a))) throw new Error('Invalid guard constants');
  const template = await readFile(path.join(here, '../native/repair-installer/guard.sh'), 'utf8');
  return template.replace('@@ROOT_CHECK@@', root ? '[ "$(/usr/bin/id -u)" = 0 ] || fail \'Preinstall requires root.\'' : '')
    .replace('@@ARCHS@@', architectures.join(' ')).replace('@@VERSION@@', version);
}
export function distribution(version, architectures) {
  const message = 'Quit Agent Orchestrator before continuing. If its updater is still running, restart your Mac and run this installer before opening AO. Use the installer for this Mac architecture on the startup volume. Downgrades, unknown versions, unrelated applications and symbolic-link destinations are refused.';
  return `<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
<title>Agent Orchestrator Repair</title>
<options customize="never" require-scripts="true" allow-external-scripts="yes" hostArchitectures="${architectures.join(',')}" rootVolumeOnly="true"/>
<domains enable_anywhere="false" enable_currentUserHome="false" enable_localSystem="true"/>
<welcome file="welcome.html" mime-type="text/html"/><conclusion file="conclusion.html" mime-type="text/html"/>
<installation-check script="checkRepair()"/>
<volume-check script="checkVolume()"/>
<script><![CDATA[
function checkRepair() {
  if (system.run('guard.sh', '', '/Applications', '/') === 0) return true;
  my.result.type = 'Fatal'; my.result.message = ${JSON.stringify(message)}; return false;
}
function checkVolume() {
  if (my.target.mountpoint === '/') return checkRepair();
  my.result.type = 'Fatal'; my.result.message = 'Install only on the startup volume.'; return false;
}
]]></script>
<choices-outline><line choice="repair"/></choices-outline>
<choice id="repair" visible="false" title="Agent Orchestrator Repair"><pkg-ref id="${packageID}"/></choice>
<pkg-ref id="${packageID}" version="${xml(version)}" auth="root">repair-component.pkg</pkg-ref>
<pkg-ref id="${packageID}"><must-close><app id="${bundleID}"/></must-close></pkg-ref>
</installer-gui-script>\n`;
}
export async function buildRepairInstaller(options, execute = run) {
  const { identity, keychainProfile, unsignedForTesting = false } = options;
  if (!options.app || !options.output) throw new Error('--app and --output are required');
  if (!unsignedForTesting && (!identity?.startsWith('Developer ID Installer: ') || !keychainProfile)) throw new Error('Developer ID Installer identity and notarization keychain profile are required');
  if (unsignedForTesting && (identity || keychainProfile)) throw new Error('Unsigned testing cannot use signing credentials');
  const app = path.resolve(options.app), output = path.resolve(options.output);
  if (!output.endsWith(unsignedForTesting ? '.unsigned.pkg' : '.pkg') || (!unsignedForTesting && output.endsWith('.unsigned.pkg'))) throw new Error('Output must end in .pkg (or .unsigned.pkg for explicit unsigned testing)');
  if (path.basename(app) !== appName || !(await lstat(app)).isDirectory() || (await lstat(app)).isSymbolicLink()) throw new Error(`Input must be a real ${appName} directory`);
  try { await lstat(output); throw new Error('Output already exists'); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  const base = path.resolve(options.dataDir || process.env.AO_DATA_DIR || path.join(homedir(), '.ao'));
  await mkdir(base, { recursive: true });
  const work = await mkdtemp(path.join(base, 'repair-installer-'));
  try {
    await execute('/bin/bash', [verifier, app]);
    const plist = path.join(app, 'Contents/Info.plist');
    const read = (key) => execute('/usr/libexec/PlistBuddy', ['-c', `Print :${key}`, plist]);
    const id = await read('CFBundleIdentifier'), version = await read('CFBundleShortVersionString'), build = await read('CFBundleVersion'), executable = await read('CFBundleExecutable');
    // Validate the executable name before constructing a path for a native tool.
    validateMetadata({ id, version, build, executable, archs: 'arm64' });
    const metadata = validateMetadata({ id, version, build, executable, archs: await execute('/usr/bin/lipo', ['-archs', path.join(app, 'Contents/MacOS', executable)]) });
    const root = path.join(work, 'payload'), scripts = path.join(work, 'scripts'), resources = path.join(work, 'resources');
    for (const directory of [root, scripts, resources]) await mkdir(directory);
    await execute('/usr/bin/ditto', [app, path.join(root, appName)]);
    await execute('/bin/bash', [verifier, path.join(root, appName)]);
    await writeFile(path.join(scripts, 'preinstall'), await renderGuard(metadata, true), { mode: 0o755 });
    await writeFile(path.join(resources, 'guard.sh'), await renderGuard(metadata), { mode: 0o755 });
    const intro = `<h1>Repair Agent Orchestrator ${xml(version)}</h1><p>Quit Agent Orchestrator. If its updater is still running, restart your Mac and run this installer before opening AO.</p><p>This replaces the entire application at /Applications/Agent Orchestrator.app. Your AO data and caches are preserved. The same version can be repaired; downgrades are refused. Installer will request permission to check the destination and running processes.</p>`;
    await writeFile(path.join(resources, 'welcome.html'), intro);
    await writeFile(path.join(resources, 'conclusion.html'), '<h1>Repair complete</h1><p>Open Agent Orchestrator from Applications when you are ready.</p>');
    const component = path.join(work, 'component.plist');
    await writeFile(component, `<?xml version="1.0"?><plist version="1.0"><array><dict><key>RootRelativeBundlePath</key><string>${appName}</string><key>BundleIsRelocatable</key><false/><key>BundleIsVersionChecked</key><false/><key>BundleHasStrictIdentifier</key><true/><key>BundleOverwriteAction</key><string>upgrade</string></dict></array></plist>`);
    const componentPkg = path.join(work, 'repair-component.pkg');
    await execute('/usr/bin/pkgbuild', ['--root', root, '--component-plist', component, '--install-location', '/Applications', '--identifier', packageID, '--version', version, '--ownership', 'recommended', '--scripts', scripts, componentPkg]);
    const definition = path.join(work, 'distribution.xml');
    await writeFile(definition, distribution(version, metadata.architectures));
    const product = path.join(work, 'repair.pkg');
    await execute('/usr/bin/productbuild', ['--distribution', definition, '--resources', resources, '--package-path', work, ...(unsignedForTesting ? [] : ['--sign', identity, '--timestamp']), product]);
    // Prove that payload packaging preserves the source app's seal and ticket.
    const expanded = path.join(work, 'expanded');
    await execute('/usr/sbin/pkgutil', ['--expand-full', product, expanded]);
    await execute('/bin/bash', [verifier, path.join(expanded, 'repair-component.pkg/Payload', appName)]);
    if (!unsignedForTesting) {
      const result = JSON.parse(await execute('/usr/bin/xcrun', ['notarytool', 'submit', product, '--keychain-profile', keychainProfile, '--wait', '--timeout', '30m', '--output-format', 'json']));
      if (result.status !== 'Accepted') throw new Error(`Notarization did not accept the installer: ${result.status}`);
      await execute('/usr/bin/xcrun', ['stapler', 'staple', product]);
      await execute('/bin/bash', [verifier, product]);
    }
    await mkdir(path.dirname(output), { recursive: true });
    await copyFile(product, output, constants.COPYFILE_EXCL);
    return output;
  } finally { await rm(work, { recursive: true, force: true }); }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const options = {};
  const names = { '--app': 'app', '--output': 'output', '--identity': 'identity', '--keychain-profile': 'keychainProfile' };
  try {
    for (let i = 2; i < process.argv.length; i++) {
      const arg = process.argv[i];
      if (arg === '--unsigned-for-testing') options.unsignedForTesting = true;
      else if (names[arg] && process.argv[i + 1] && !process.argv[i + 1].startsWith('--')) options[names[arg]] = process.argv[++i];
      else throw new Error(`Unknown or incomplete argument: ${arg}`);
    }
    console.log(await buildRepairInstaller(options));
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
