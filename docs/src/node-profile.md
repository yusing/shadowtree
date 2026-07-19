# Node Profile

The Node profile is selected when:

- `--profile node` is provided
- config has `profile = "node"`
- no config is loaded and Shadowtree detects the nearest `package.json` upward
  from the current directory

Node built-ins resolve the nearest `package.json` directory and generate shell
commands that `cd` there before invoking the package manager or tool. This
makes subdirectory invocation run against the package root.

Node built-ins currently reject `--all`. npm, pnpm, Yarn, and Bun expose
different workspace selection and execution contracts, so aggregate support
must be defined per generated recipe rather than inferred as directory
fan-out.

Every Node built-in recipe has `sandboxed = false` by default because package
manager and framework commands commonly mutate lockfiles, dependency state,
caches, and generated outputs.

## Package Manager Detection

Detection order:

1. `packageManager` prefix: `pnpm`, `yarn`, `bun`, or `npm`
2. lockfiles: `pnpm-lock.yaml`, `yarn.lock`, `bun.lockb`, `bun.lock`,
   `package-lock.json`, `npm-shrinkwrap.json`
3. default `npm`

## Built-In Recipes

```text
install    npm|pnpm|yarn|bun install
dev        package script dev, or inferred framework dev command
build      package script build, or inferred framework build command
start      package script start, or inferred framework start/preview command
test       package script test, or Vitest/Jest/Playwright/Bun fallback
lint       package script lint, or ESLint/Oxlint/Biome
fmt        package script fmt/format, or Prettier/Oxfmt/Biome
typecheck  package script typecheck/type-check, or detected type checkers
check      available lint, typecheck, and test recipes in that order
```

Package scripts fill gaps without overriding predefined Node recipe names.

## Command Forms

- Package scripts run `<pm> run <script> -- {@}`.
- Tool commands run `npm exec -- <bin> ... {@}`,
  `pnpm exec <bin> ... {@}`, `yarn exec <bin> ... {@}`, or
  `bunx <bin> ... {@}`.
- Bun projects without a test script use `bun test {@}` when Vitest is not
  installed.

## Framework Inference

For `dev`, `build`, and `start`:

- `next`: `next dev`, `next build`, `next start`
- `vite`: `vite`, `vite build`, `vite preview`
- `nuxt`: `nuxt dev`, `nuxt build`, `nuxt preview`
- `astro`: `astro dev`, `astro build`, `astro preview`
- `@sveltejs/kit`: `vite`, `vite build`, `vite preview`

## Test Inference

- Script `test` wins.
- Bun projects use installed `vitest` first, otherwise `bun test`.
- Other projects use installed `vitest`, then `jest`, then `playwright test`
  when `@playwright/test` is installed.

## Lint Inference

- Script `lint` wins.
- ESLint markers: `eslint` dependency, `eslint.config.*`, `.eslintrc*`, or
  package `eslintConfig`; command `eslint .`.
- Oxlint markers: `oxlint` dependency, `oxlint.config.*`, `.oxlintrc.json`, or
  `.oxlintrc.jsonc`; command `oxlint`.
- Biome markers: `@biomejs/biome` dependency, `biome.json`, or `biome.jsonc`;
  command `biome lint .`.

## Format Inference

- Script `fmt` wins, then script `format`.
- Prettier markers: `prettier` dependency, `prettier.config.*`,
  `.prettierrc*`, or package `prettier`; command `prettier --write .`.
- Oxfmt markers: `oxfmt` dependency, `oxfmt.config.*`, `.oxfmtrc.json`, or
  `.oxfmtrc.jsonc`; command `oxfmt`.
- Biome markers: `@biomejs/biome` dependency, `biome.json`, or `biome.jsonc`;
  command `biome format --write .`.

## Typecheck Inference

- Script `typecheck` wins, then script `type-check`.
- Otherwise Shadowtree runs every detected checker in stable order:
  `vue-tsc --noEmit`, `svelte-check`, then `tsc --noEmit`.
- `tsc --noEmit` is included when `typescript` is installed or `tsconfig.json`
  exists.

## Script Recipe Names

Before a package script becomes a recipe name, Shadowtree replaces `:` and
every character outside `[A-Za-z0-9._-]` with `-`, collapses repeated `-`, trims
leading and trailing `-`, and skips empty or reserved names.

If multiple scripts normalize to the same recipe name, the script whose
original name already equals the normalized name wins; otherwise the
lexicographically first original script name wins. For example, package script
`lint:fix` becomes recipe `lint-fix`, but the generated command still runs the
original script key `lint:fix`.
