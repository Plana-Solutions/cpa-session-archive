import path from 'node:path';
import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { viteSingleFile } from 'vite-plugin-singlefile';

/**
 * CPAMP injects an unlayered <style id="cpamp-plugin-host-style"> into the
 * plugin iframe <head> (using :where(...) selectors with 0,0,0 specificity)
 * before the plugin's own stylesheet. Per the CSS Cascade Layers spec, any
 * unlayered author rule beats every @layer rule regardless of specificity or
 * source order, so Tailwind v4's @layer base/utilities lose to CPAMP's light
 * host theme. Unwrapping @layer blocks in exact source order preserves
 * Tailwind's theme -> base -> components -> utilities cascade while letting
 * our stylesheet win naturally over CPAMP's earlier 0,0,0 host defaults.
 */
export function unwrapCssLayers(css: string): string {
  let out = '';
  let i = 0;
  const n = css.length;

  const isIdentChar = (ch: string | undefined) =>
    ch !== undefined && /[a-zA-Z0-9_-]/.test(ch);

  while (i < n) {
    const ch = css[i];

    // Preserve strings verbatim
    if (ch === '"' || ch === "'") {
      const quote = ch;
      const start = i++;
      while (i < n) {
        if (css[i] === '\\') {
          i += 2;
        } else if (css[i] === quote) {
          i++;
          break;
        } else {
          i++;
        }
      }
      out += css.slice(start, i);
      continue;
    }

    // Preserve comments verbatim
    if (ch === '/' && css[i + 1] === '*') {
      const end = css.indexOf('*/', i + 2);
      if (end === -1) {
        out += css.slice(i);
        break;
      }
      out += css.slice(i, end + 2);
      i = end + 2;
      continue;
    }

    // Match @layer at-rule
    if (
      ch === '@' &&
      css.slice(i, i + 6).toLowerCase() === '@layer' &&
      !isIdentChar(css[i - 1]) &&
      !isIdentChar(css[i + 6])
    ) {
      let j = i + 6;
      while (j < n && css[j] !== '{' && css[j] !== ';') {
        if (css[j] === '/' && css[j + 1] === '*') {
          const end = css.indexOf('*/', j + 2);
          j = end === -1 ? n : end + 2;
        } else if (css[j] === '"' || css[j] === "'") {
          const quote = css[j++];
          while (j < n) {
            if (css[j] === '\\') j += 2;
            else if (css[j] === quote) {
              j++;
              break;
            } else j++;
          }
        } else {
          j++;
        }
      }

      if (j < n && css[j] === ';') {
        // Drop layer order statement (e.g. `@layer components;`)
        i = j + 1;
        continue;
      }

      if (j < n && css[j] === '{') {
        // Find matching closing brace for the @layer block
        const bodyStart = j + 1;
        let depth = 1;
        j = bodyStart;
        while (j < n && depth > 0) {
          const c = css[j];
          if (c === '"' || c === "'") {
            const quote = c;
            j++;
            while (j < n) {
              if (css[j] === '\\') j += 2;
              else if (css[j] === quote) {
                j++;
                break;
              } else j++;
            }
          } else if (c === '/' && css[j + 1] === '*') {
            const end = css.indexOf('*/', j + 2);
            j = end === -1 ? n : end + 2;
          } else if (c === '{') {
            depth++;
            j++;
          } else if (c === '}') {
            depth--;
            j++;
          } else {
            j++;
          }
        }
        const bodyEnd = depth === 0 ? j - 1 : j;
        out += unwrapCssLayers(css.slice(bodyStart, bodyEnd));
        i = j;
        continue;
      }
    }

    out += ch;
    i++;
  }

  return out;
}

function unlayerCssPlugin(): Plugin {
  let isServe = false;
  return {
    name: 'unlayer-css-for-cpamp-host',
    enforce: 'post',
    configResolved(config) {
      isServe = config.command === 'serve';
    },
    transform(code, id) {
      if (!isServe) return null;
      const cleanId = id.split('?', 1)[0];
      if (!cleanId.endsWith('.css') || !code.includes('@layer')) return null;
      return { code: unwrapCssLayers(code), map: null };
    },
    generateBundle: {
      order: 'post',
      handler(_options, bundle) {
        for (const item of Object.values(bundle)) {
          if (item.type === 'asset' && item.fileName.endsWith('.css')) {
            const raw =
              typeof item.source === 'string'
                ? item.source
                : Buffer.from(item.source).toString('utf8');
            if (raw.includes('@layer')) {
              item.source = unwrapCssLayers(raw);
            }
          }
        }
      },
    },
  };
}

export default defineConfig({
  root: path.resolve(import.meta.dirname, 'web'),
  plugins: [react(), tailwindcss(), unlayerCssPlugin(), viteSingleFile()],
  build: {
    outDir: path.resolve(import.meta.dirname, 'internal/archive/web'),
    emptyOutDir: false,
    target: 'es2020',
    assetsInlineLimit: 100000000,
    cssCodeSplit: false,
    rollupOptions: {
      input: path.resolve(import.meta.dirname, 'web/index.html'),
    },
  },
});
