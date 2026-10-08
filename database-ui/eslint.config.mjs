// ESLint flat config: Rancher Shell's shared ruleset for UI extensions
// (eslint.config.base.mjs ships with @rancher/shell), plus this repo's settings.
import globals from 'globals';
import shellConfig from '@rancher/shell/eslint.config.base.mjs';

export default [
  ...shellConfig,
  {
    ignores: [
      // Build tooling and generated output
      'babel.config.js',
      'vue.config.js',
      'jest.config.js',
      'pkg/*/babel.config.js',
      'pkg/*/vue.config.js',
      'pkg/*/.shell/**',
    ],
  },
  {
    // Unit tests (jest)
    files:           ['pkg/**/__tests__/**/*.js'],
    languageOptions: { globals: { ...globals.jest, ...globals.node } },
  },
];
