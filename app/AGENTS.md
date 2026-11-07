# Penguin app conventions

- Keep desktop integration in Go and rendering in `frontend/`. Expose narrow services through generated Wails bindings when features need them.
- Place domain and provider logic under Go's `internal/` directory as it is implemented. Do not add empty packages, mock product features, or speculative abstractions.
- Keep credentials out of frontend storage, generated bindings, logs, and repository files.
- Use shadcn/ui components for UI elements unless the user explicitly requests a custom component. Reuse the existing components in `frontend/src/components/ui/`.
- Use strict TypeScript, accessible controls, and React state only where interaction requires it. Do not add effects or memoization to static components.
- Use gofmt and go vet. Propagate errors and use contexts for cancellable network/model work; avoid unbounded background goroutines.
- Add behavioral tests when implementing generation, provider requests, cancellation, downloads, and exports. Do not add tests that only repeat static markup or configuration.
- Add E2E journeys under `frontend/e2e/` using the shared Playwright fixtures, accessible locators, and retrying assertions. Run root `pnpm test:e2e` when UI, asset-serving, or E2E tooling changes. Do not treat headless server-mode tests as native desktop integration coverage.
- Build frontend assets before direct Go compilation because `main.go` embeds `frontend/dist`.
- Run root `pnpm check` and `pnpm build` before handing off app changes.
