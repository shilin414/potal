import { defineConfig } from 'vitest/config';
import path from 'path';

export default defineConfig({
  resolve: { alias: { '@': path.resolve(__dirname, './src') } },
  test: {
    // 默认 node 环境跑纯逻辑单测；需要 DOM 的组件测试在文件头用
    // `// @vitest-environment jsdom` 单独声明（如 __tests__/scheduleEditorPayload.test.tsx）。
    environment: 'node',
    include: ['src/**/__tests__/**/*.test.ts', 'src/**/__tests__/**/*.test.tsx'],
  },
});
