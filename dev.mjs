import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const cliRoot = new URL('./node_modules/@dbx-app/plugin-cli/', import.meta.url);
if (process.argv[2] === '--config') {
  const options = JSON.parse(process.argv[3]);
  // CLI 0.1.9 hardcodes Go 1.22 in dev workspaces; use this project's pinned SDK instead.
  if (options.commands?.backend) delete options.commands.backend.goWorkspace;
  const { startDevelopment } = await import(new URL('dev-runtime/runtime.mjs', cliRoot));
  await startDevelopment(options);
} else {
  const result = spawnSync(process.execPath, [fileURLToPath(new URL('bin/dbx-plugin.js', cliRoot)), 'dev', '--path', '.', ...process.argv.slice(2)], {
    stdio: 'inherit', env: { ...process.env, DBX_PLUGIN_DEV_RUNTIME: fileURLToPath(import.meta.url), GOWORK: 'off' }
  });
  if (result.error) throw result.error;
  process.exitCode = result.status ?? 1;
}
