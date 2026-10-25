import js from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["src/gen/**", "dist-pages/**", "test-results/**", "playwright-report/**"] },
  js.configs.recommended,
  ...tseslint.configs.strict,
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: { globals: globals.browser },
    plugins: { "react-hooks": reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // Cluster data (names, labels, images) is untrusted: render it as text
      // only (ADR-0007, threat model boundary C).
      "no-restricted-syntax": [
        "error",
        { selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']", message: "Render cluster data as text, never HTML." },
        { selector: "AssignmentExpression[left.property.name=/^(innerHTML|outerHTML)$/]", message: "Render cluster data as text, never HTML." },
        { selector: "CallExpression[callee.property.name='insertAdjacentHTML']", message: "Render cluster data as text, never HTML." },
      ],
      "no-restricted-globals": ["error", { name: "eval", message: "No eval." }],
    },
  },
  {
    // Plain scripts served as they are (public/theme.js) run in the browser.
    files: ["public/**/*.js"],
    languageOptions: { globals: globals.browser, sourceType: "script" },
  },
  {
    // In tests a wrong assumption should fail loudly, so `!` is fine there.
    files: ["**/*.test.ts"],
    rules: { "@typescript-eslint/no-non-null-assertion": "off" },
  },
);
