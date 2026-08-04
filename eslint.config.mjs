import js from '@eslint/js';
import globals from 'globals';

// The plugin's browser scripts are plain script-scope files: Unraid's settings
// page loads them with a bare <script src>, with no bundler and no module
// system. So they are linted as `sourceType: 'script'` with browser globals, and
// their top-level functions are DELIBERATE globals (the page calls them) rather
// than accidental leaks - `no-implicit-globals` would flag every one of them and
// is left off for that reason.
export default [
    {
        ignores: ['node_modules/**', 'vendor/**', 'bin/**', 'release/**'],
    },
    {
        // This config file itself is an ES module; everything else in the repo
        // is script or commonjs, so it needs its own entry.
        files: ['**/*.mjs'],
        languageOptions: { ecmaVersion: 2022, sourceType: 'module' },
    },
    js.configs.recommended,
    {
        files: ['src/usr/local/emhttp/plugins/watch-aware-preloader/js/*.js'],
        languageOptions: {
            ecmaVersion: 2022,
            sourceType: 'script',
            globals: {
                ...globals.browser,
                // Each script guards a CommonJS export so the pure helpers can be
                // required by the headless tests; `module` is otherwise absent.
                module: 'readonly',
            },
        },
        rules: {
            // 'smart' keeps `x != null` legal: that is the deliberate
            // null-or-undefined test the meter uses against optional JSON
            // fields, not a loose-equality slip.
            eqeqeq: ['error', 'smart'],
            'no-var': 'error',
            'prefer-const': 'error',
            'no-unused-vars': ['error', { argsIgnorePattern: '^_' }],
            // The meter and order scripts build strings and arrays inside loops;
            // flag the accidental O(n^2) rebuild that bot review caught by hand.
            'no-loop-func': 'error',
        },
    },
    {
        // Node-side: the headless tests, which require() the pure helpers.
        files: ['test/**/*.js'],
        languageOptions: {
            ecmaVersion: 2022,
            sourceType: 'commonjs',
            globals: {
                ...globals.node,
            },
        },
        rules: {
            eqeqeq: ['error', 'always'],
            'no-var': 'error',
            'prefer-const': 'error',
        },
    },
];
