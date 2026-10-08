import fs from 'fs';
import path from 'path';
import jsyaml from 'js-yaml';

const ROOT = path.resolve(__dirname, '..');
const strings = jsyaml.load(fs.readFileSync(path.join(ROOT, 'l10n/en-us.yaml'), 'utf8'));

function flatten(obj, prefix = '', out = {}) {
  Object.entries(obj).forEach(([k, v]) => {
    if (v && typeof v === 'object') {
      flatten(v, `${ prefix }${ k }.`, out);
    } else {
      out[`${ prefix }${ k }`] = v;
    }
  });

  return out;
}

function sourceFiles(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);

    if (e.isDirectory()) {
      return ['node_modules', '__tests__', '.shell'].includes(e.name) ? [] : sourceFiles(p);
    }

    return /\.(vue|js|ts)$/.test(e.name) ? [p] : [];
  });
}

const flat = flatten(strings);

describe('en-us.yaml', () => {
  // Shell's t() HTML-escapes, and Vue/Banner escape again, so ' and " would
  // show as &#39; / &quot;
  it('has no straight quotes or apostrophes in string values', () => {
    const bad = Object.entries(flat).filter(([, v]) => /['"]/.test(String(v))).map(([k]) => k);

    expect(bad).toEqual([]);
  });

  it('defines every dbaas.* key the code uses', () => {
    const used = new Set();

    sourceFiles(ROOT).forEach((file) => {
      const src = fs.readFileSync(file, 'utf8');

      for (const m of src.matchAll(/['"`](dbaas\.[a-zA-Z0-9_.]+)['"`]/g)) {
        // Kubernetes type IDs (dbaas.opencloud.wso2.com.<kind>) are not translation keys
        if (!m[1].startsWith('dbaas.opencloud.')) {
          used.add(m[1]);
        }
      }
    });

    // Keys built at runtime (e.g. dbaas.restore.stage.<stage>) end with a dot
    const missing = [...used].filter((k) => !k.endsWith('.') && !(k in flat) && !Object.keys(flat).some((f) => f.startsWith(`${ k }.`)));

    expect(missing).toEqual([]);
  });
});
