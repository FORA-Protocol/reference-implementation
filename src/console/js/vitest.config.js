import { defineConfig } from 'vitest/config';

// The scripts under test run in the browser, but the module the tests import
// uses nothing of the page: only WebCrypto, which Node provides as the same
// globalThis.crypto.subtle interface.
export default defineConfig({
  test: {
    environment: 'node',
    include: ['tests/**/*.test.js'],
  },
});
