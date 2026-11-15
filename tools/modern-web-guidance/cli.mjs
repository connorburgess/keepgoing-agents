import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const packageDir = fileURLToPath(new URL('./node_modules/modern-web-guidance/', import.meta.url));
const repo = fileURLToPath(new URL('../../', import.meta.url));
const [command, ...args] = process.argv.slice(2);
if (!['search', 'retrieve', 'legacy-scan'].includes(command) ||
    (command === 'legacy-scan' && args.length !== 0) ||
    (command !== 'legacy-scan' && (args.length !== 1 || args[0].startsWith('-')))) {
  console.error('Usage: node tools/modern-web-guidance/cli.mjs search "topic" | retrieve "guide-id" | legacy-scan');
  process.exit(2);
}
const env = { ...process.env, DISABLE_TELEMETRY: '1', ENABLE_FILE_LOGGING: 'false' };
delete env.NODE_OPTIONS;
delete env.NODE_PATH;
const guidanceDir = fileURLToPath(new URL('./', import.meta.url));
const flags = ['--experimental-permission', '--allow-fs-read=' + packageDir];
if (command !== 'legacy-scan') flags.push('--allow-fs-read=' + path.join(guidanceDir, '.mwgrc'));
if (command === 'legacy-scan') flags.push('--allow-fs-read=' + path.join(repo, 'cmd/looptender/dockui'));
const result = spawnSync(process.execPath, [
  ...flags,
  path.join(packageDir, 'skills/modern-web-guidance/modern-web.mjs'),
  command,
  ...(command === 'legacy-scan' ? ['cmd/looptender/dockui', '--json'] : args),
], { cwd: command === 'legacy-scan' ? repo : guidanceDir, env, encoding: 'utf8', timeout: 60000, maxBuffer: 4 * 1024 * 1024 });
if (result.stdout) process.stdout.write(result.stdout);
if (result.stderr) process.stderr.write(result.stderr);
if (result.error) console.error('Guidance CLI failed: ' + result.error.message);
process.exit(result.error ? 1 : result.status ?? 1);
