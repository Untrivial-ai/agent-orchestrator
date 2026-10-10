// Build existing Forge/Vite targets without packaging or opening a dev server.
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const { createHash } = require('node:crypto');
const { execFileSync } = require('node:child_process');

for (const key of Object.keys(process.env)) {
  if (key.startsWith('AO_')) delete process.env[key];
}

(async () => {
  const frontend = path.resolve(process.argv[2]);
  process.chdir(frontend);
  const localRequire = createRequire(path.join(frontend, 'package.json'));
  await localRequire('@electron/rebuild').rebuild({
    buildPath: frontend, electronVersion: localRequire('electron/package.json').version,
    onlyModules: ['better-sqlite3'], force: false,
  });
  const vite = localRequire('vite');
  const forge = await vite.loadConfigFromFile(
    { command: 'build', mode: 'production' }, path.join(frontend, 'forge.config.ts'),
  );
  const plugin = forge.config.plugins.find((entry) => entry.name === 'vite');
  if (!plugin) throw new Error('Forge Vite plugin is missing');
  const Generator = localRequire('@electron-forge/plugin-vite/dist/ViteConfig.js').default;
  const generator = new Generator(plugin.config, frontend, true);
  for (const config of await generator.getBuildConfigs()) {
    await vite.build({ ...config, configFile: false });
  }
  for (const config of await generator.getRendererConfig()) {
    await vite.build({ ...config, configFile: false });
  }
  const build = path.join(frontend, '.vite/build');
  fs.renameSync(path.join(build, 'main.js'), path.join(build, 'ao-main.cjs'));
  fs.copyFileSync(path.join(__dirname, 'bootstrap.cjs'), path.join(build, 'main.js'));
  const hash = (file) => createHash('sha256').update(fs.readFileSync(file)).digest('hex');
  const files = ['.vite/build/main.js', '.vite/build/ao-main.cjs',
    '.vite/build/preload.js', '.vite/build/annotate-preload.js',
    '.vite/renderer/main_window/index.html'];
  fs.writeFileSync(path.join(frontend, '.vite/testing-target.json'), JSON.stringify({
    commitSHA: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: frontend, encoding: 'utf8' }).trim(),
    electronVersion: localRequire('electron/package.json').version,
    files: Object.fromEntries(files.map((file) => [file, hash(path.join(frontend, file))])),
  }, null, 2));
})().catch((error) => { console.error(error); process.exitCode = 1; });
