import js from '@eslint/js';
import globals from 'globals';
import tseslint from 'typescript-eslint';

// ADR0102 Phase A：仅收敛规则集，不改产品语义;ts 严格规则随 strict ratchet 分阶段开启。
export default [
  {
    ignores: [
      '**/node_modules/**',
      '**/dist/**',
      'coverage/**',
      '.marshal/**',
      'apps/**',
      '**/scripts/experience-*',
      'docs/**',
      '**/*.txt'
    ]
  },
  js.configs.recommended,
  {
    files: ['**/*.ts', '**/*.mjs', '**/*.js'],
    languageOptions: {
      ecmaVersion: 'latest',
      sourceType: 'module',
      globals: {...globals.node},
      parser: tseslint.parser
    },
    plugins: {
      '@typescript-eslint': tseslint.plugin
    },
    rules: {
      'no-unused-vars': 'off',
      'no-empty': ['error', {allowEmptyCatch: true}],
      'no-control-regex': 'off',
      'no-useless-escape': 'off',
      'no-constant-condition': ['error', {checkLoops: false}]
    }
  }
];
