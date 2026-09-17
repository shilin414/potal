module.exports = {
  root: true,
  env: { browser: true, es2021: true, node: true },
  parser: '@typescript-eslint/parser',
  parserOptions: {
    ecmaVersion: 'latest',
    sourceType: 'module',
    ecmaFeatures: { jsx: true },
  },
  plugins: ['@typescript-eslint', 'react-hooks', 'react-refresh'],
  extends: ['eslint:recommended', 'plugin:@typescript-eslint/recommended'],
  ignorePatterns: ['dist', 'node_modules', '*.config.js', '*.config.ts'],
  rules: {
    '@typescript-eslint/no-explicit-any': 'off',
    '@typescript-eslint/no-unused-vars': ['error', {
      argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrorsIgnorePattern: '^_'
    }],
    '@typescript-eslint/no-empty-function': 'off',
    'react-hooks/rules-of-hooks': 'error',
    'react-hooks/exhaustive-deps': 'warn',
    'react-refresh/only-export-components': 'off',
  },
  overrides: [
    {
      // 三次复审 P2-R2: every applications URL must live in
      // src/services/runApi.ts. This is the static half of the /v2 vs
      // legacy-list CI guard: runApi.contract.test.ts can only pin the
      // paths the code under test actually issues, but a hand-written
      // `api.get('/v2/applications')` (the whole-catalog legacy array) or
      // a direct by-id call in a page would bypass it. Centralizing the
      // URLs in runApi keeps ONE place to enforce the /v2 contract.
      files: ['src/**/*.{ts,tsx}'],
      excludedFiles: ['src/services/**', '**/__tests__/**'],
      rules: {
        'no-restricted-syntax': ['error',
          {
            selector: 'Literal[value=/v2\\/applications/]',
            message: "applications API 必须经由 services/runApi.ts 调用；业务代码禁止直连该 URL（三次复审 P2-R2）",
          },
          {
            selector: 'TemplateElement[value.raw=/v2\\/applications/]',
            message: "applications API 必须经由 services/runApi.ts 调用；业务代码禁止直连该 URL（三次复审 P2-R2）",
          },
        ],
      },
    },
  ],
};
