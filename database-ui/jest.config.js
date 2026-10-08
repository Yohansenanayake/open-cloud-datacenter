// Jest for the extension's unit tests (pkg/database-ui/__tests__). Babel uses
// the repo's babel.config.js (Rancher Shell's), which in the test environment
// needs babel-plugin-transform-require-context and babel-plugin-istanbul.
module.exports = {
  roots:                  ['<rootDir>/pkg'],
  testEnvironment:        'jsdom',
  testEnvironmentOptions: { customExportConditions: ['node', 'node-addons'] },
  watchman:               false,
  moduleFileExtensions:   ['js', 'ts', 'json', 'vue'],
  moduleNameMapper:       {
    '^@shell/(.*)$':      '<rootDir>/node_modules/@rancher/shell/$1',
    '^@components/(.*)$': '<rootDir>/node_modules/@rancher/shell/rancher-components/$1',
    '\\.(css|scss)$':     '<rootDir>/pkg/database-ui/__tests__/helpers/style-mock.js',
  },
  transform: {
    '^.+\\.vue$':  '@vue/vue3-jest',
    '^.+\\.(t|j)s$': 'babel-jest',
  },
  transformIgnorePatterns: ['/node_modules/(?!@rancher/shell).+\\.js$'],
  testMatch:               ['**/__tests__/**/*.test.js'],
  testPathIgnorePatterns:  ['/node_modules/', '/\\.shell/'],
};
