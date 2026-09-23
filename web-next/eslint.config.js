//  @ts-check

import { tanstackConfig } from "@tanstack/eslint-config"

export default [
  ...tanstackConfig,
  {
    rules: {
      "import/no-cycle": "off",
      "import/order": "off",
      "sort-imports": "off",
      "@typescript-eslint/array-type": "off",
      "@typescript-eslint/require-await": "off",
      "pnpm/json-enforce-catalog": "off",
    },
  },
  {
    // tsconfig's `@cloudflare/workers-types` (for src/server.ts) gives every
    // Response a generic `json<T>()`, which infers `T` from an `as T`, so this
    // rule now calls `(await response.json()) as T` unnecessary. Elsewhere
    // that read is written `const data: T = await response.json()`; this
    // file keeps the old form until its next change.
    files: ["src/components/workspace-tools-api.ts"],
    rules: { "@typescript-eslint/no-unnecessary-type-assertion": "off" },
  },
  {
    ignores: ["eslint.config.js", ".prettierrc", "src/routeTree.gen.ts"],
  },
]
