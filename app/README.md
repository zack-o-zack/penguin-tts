# Penguin desktop app

Go/Wails desktop shell with a React + TypeScript frontend. Wails v3.1.0 and its matching JavaScript runtime are pinned. The initial target is Apple Silicon macOS; desktop platform build recipes are retained for future Windows/Linux validation.

Install dependencies once from the repository root with `pnpm install`. The app and frontend belong to the root pnpm workspace; do not create another lockfile here.

From the root, use `pnpm dev`, `pnpm build`, and `pnpm check`. From this directory, use `pnpm dev`, `pnpm build`, `pnpm package`, or `pnpm bindings`. The Wails CLI is a Go tool dependency, so these commands do not require a global CLI installation.

`pnpm build` creates the native binary under `bin/`; `pnpm package` creates the platform package (a locally ad-hoc-signed `.app` on macOS). This is development packaging, not Mac App Store signing or sandbox validation.

The frontend builds into `frontend/dist`, which Go embeds. Run root `pnpm build:ui` before invoking Go directly on a fresh checkout. The app test command does this automatically before Go vet and tests.

`build/` contains desktop platform recipes and starter packaging assets from the Wails template. The bundle identifier, icon, signing, entitlements, and distribution metadata must be reviewed before release. Generated bindings, compiled assets, and binaries are ignored.

Add real domain code and tests as features are implemented. No speech engine, API-key storage, or inference runtime is connected in this boilerplate.

## End-to-end tests

Run root `pnpm test:e2e:install` once, then `pnpm test:e2e`. Playwright uses the production frontend served by the actual Go app in headless server mode; it does not launch a native window. The native desktop build is checked separately in CI. See the [E2E guide](frontend/e2e/README.md) for coverage, failure artifacts, and testing conventions.
