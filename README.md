# Penguin TTS

An open-source desktop app for speech generation with local models, your own cloud API keys, and compatible self-hosted servers.

This repository currently contains the desktop app shell and development tooling. Speech generation is not implemented yet.

## Workspace

```text
app/              Go/Wails desktop app
  frontend/       React + TypeScript + Vite UI
.aislop/          Repository-wide quality gate
.husky/           Repository-wide Git hooks
.spikes/          Product requirements and research
```

The frontend includes the full shadcn/ui Radix Nova component set and a charcoal/mint Penguin theme. See the [UI guide](app/frontend/README.md) for imports, theme tokens, and accessibility coverage.

One pnpm workspace and lockfile manage JavaScript dependencies. The desktop app has its own Go module and checks. Add shared packages when there is code to share.

## Requirements

- Node.js 24 (see `.node-version`).
- pnpm 10.28.1 (the root `packageManager` field pins it).
- Go 1.27.3.
- macOS 13.3 or newer: Xcode Command Line Tools (`xcode-select --install`).

The initial target is Apple Silicon macOS. Windows and Linux development also requires platform-specific native dependencies.

## Start

```sh
pnpm install
pnpm dev
```

`pnpm install` installs the root pre-commit hook through Husky. The Wails CLI is pinned by the app's Go module; no global Wails installation is needed. The first desktop run downloads Go dependencies and compiles native code.

For frontend-only development (Go is needed to generate the Wails types first):

```sh
pnpm dev:ui
```

## Commands

Run these from the repository root:

| Command                 | Purpose                                                       |
| ----------------------- | ------------------------------------------------------------- |
| `pnpm dev`              | Start the desktop app with frontend hot reload                |
| `pnpm dev:ui`           | Start only the Vite frontend                                  |
| `pnpm build`            | Build the desktop app                                         |
| `pnpm build:ui`         | Type-check and build frontend assets                          |
| `pnpm check`            | Run formatting/lint checks, type checks, Go tests, and aislop |
| `pnpm format`           | Apply supported formatting and safe lint fixes                |
| `pnpm slop`             | Run aislop's full-repository quality gate                     |
| `pnpm slop:staged`      | Run the same quality gate on staged files                     |
| `pnpm test:e2e:install` | Install the pinned Chromium and WebKit test browsers          |
| `pnpm test:e2e`         | Build and run E2E tests against the Go/Wails host             |
| `pnpm test:e2e:ui`      | Run E2E tests in Playwright's interactive UI                  |
| `pnpm test:e2e:report`  | Open the last HTML E2E report                                 |

The root pre-commit hook runs lint-staged (Biome, Prettier, and gofmt), then `aislop ci --staged`. The aislop configuration applies to the entire monorepo and rejects a score below 85. CI repeats the full checks and build; a local hook is not the only enforcement point.

Aislop reports skipped optional engines in its output. Knip and golangci-lint are not installed by this setup; TypeScript checking, Biome, gofmt, and Go vet run through the workspace commands.

E2E tests run independently from `pnpm check` and the pre-commit hook. CI runs both the native build and the Chromium/WebKit suite, retaining reports and failure artifacts.

Generated frontend assets, bindings, native binaries, dependencies, and local secrets stay out of Git. Commit `pnpm-lock.yaml`, `app/go.mod`, and `app/go.sum` when dependencies change. The project license has not been selected yet.
