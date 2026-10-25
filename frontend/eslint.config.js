// @ts-check
const eslint = require('@eslint/js');
const { defineConfig } = require('eslint/config');
const tseslint = require('typescript-eslint');
const angular = require('angular-eslint');
const importX = require('eslint-plugin-import-x');
const { createTypeScriptImportResolver } = require('eslint-import-resolver-typescript');

module.exports = defineConfig([
  {
    files: ['**/*.ts'],
    extends: [
      eslint.configs.recommended,
      tseslint.configs.recommended,
      tseslint.configs.stylistic,
      angular.configs.tsRecommended,
    ],
    processor: angular.processInlineTemplates,
    plugins: { 'import-x': importX },
    settings: {
      'import-x/extensions': ['.ts'],
      'import-x/parsers': { '@typescript-eslint/parser': ['.ts'] },
      'import-x/resolver-next': [createTypeScriptImportResolver()],
    },
    rules: {
      // Files that import each other in a circle are hard to follow and can load
      // in the wrong order, so a cycle anywhere in the app fails the lint.
      'import-x/no-cycle': 'error',
      '@angular-eslint/directive-selector': [
        'error',
        {
          type: 'attribute',
          prefix: 'app',
          style: 'camelCase',
        },
      ],
      '@angular-eslint/component-selector': [
        'error',
        {
          type: 'element',
          prefix: 'app',
          style: 'kebab-case',
        },
      ],
    },
  },
  {
    files: ['**/*.html'],
    extends: [angular.configs.templateRecommended, angular.configs.templateAccessibility],
    rules: {},
  },
]);
