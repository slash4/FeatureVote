// Compiles a TypeScript source module with esbuild (bundled, ESM) and imports it, so tests exercise
// the real widget modules without a separate compile step.
import * as esbuild from 'esbuild';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

export const widgetDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export async function importTs(rel) {
  const out = await esbuild.build({
    entryPoints: [path.join(widgetDir, rel)],
    bundle: true,
    format: 'esm',
    platform: 'neutral',
    write: false,
    logLevel: 'silent',
  });
  const code = out.outputFiles[0].text;
  return import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'));
}
