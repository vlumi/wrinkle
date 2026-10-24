import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["node_modules/", "src/gen/"] },
  js.configs.recommended,
  ...tseslint.configs.strictTypeChecked,
  {
    languageOptions: {
      globals: globals.node,
      parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname },
    },
    rules: {
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
      "@typescript-eslint/no-non-null-assertion": "off",
      "@typescript-eslint/restrict-template-expressions": [
        "error",
        { allowNumber: true, allow: [{ from: "lib", name: "bigint" }] },
      ],
      "@typescript-eslint/no-confusing-void-expression": ["error", { ignoreArrowShorthand: true }],
    },
  },
  // node:test registers tests by calling test(); its promise is the runner's.
  { files: ["**/*.test.ts"], rules: { "@typescript-eslint/no-floating-promises": "off" } },
  { files: ["**/*.js"], ...tseslint.configs.disableTypeChecked },
);
