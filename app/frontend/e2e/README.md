# End-to-end tests

Playwright runs the production frontend through the actual Go/Wails asset server, compiled with `server,production` build tags. It does not use Vite's development server or replace the backend with a mock. Chromium and WebKit run the same tests; WebKit provides additional coverage relevant to the macOS webview.

## Run

From the monorepo root:

```sh
pnpm install
pnpm test:e2e:install
pnpm test:e2e
```

The root test command generates Wails bindings and builds the frontend in `e2e` mode before Playwright starts. This mode adds `e2e/components.html` for component accessibility tests; normal desktop builds exclude that fixture. Playwright compiles and starts the headless Go app on `127.0.0.1:9473`, waits for HTTP readiness, and shuts down its process tree afterward. No running development app is required. Port conflicts fail the run; an unrelated or stale server is never reused.

```sh
pnpm test:e2e --project=webkit
pnpm test:e2e:ui
pnpm test:e2e:report
```

Browser binaries are pinned by the Playwright version in the workspace lockfile. Run the install command again when upgrading Playwright. CI installs required browser dependencies with `--with-deps`.

## Current coverage

- Production bundle startup and reload without third-party HTTP dependencies.
- Unhandled JavaScript exceptions, console errors, failed application requests, and HTTP asset errors fail the test.
- Minimum desktop window size and reflow at 320 CSS pixels without horizontal overflow.
- Automated axe WCAG A/AA checks through WCAG 2.2 against representative components in both brand palettes.
- Keyboard operation for checkboxes, switches, selects, sliders, tabs, and accordions; dialog focus trapping, Escape, and focus restoration.
- Visible keyboard focus and reduced-motion behavior.

This suite covers the shared UI and Go asset-serving path. It does not validate native window behavior, macOS sandbox entitlements, Keychain, native audio devices, model inference, or cloud providers. The desktop build remains a separate CI check. Add native integration tests and real product journeys when those features exist; automated accessibility checks also do not replace keyboard and VoiceOver testing.

## Test conventions

- Import `test` and `expect` from `./fixtures` to retain the browser-error checks.
- Use role/label locators and Playwright's retrying assertions. Avoid CSS selectors tied to layout, arbitrary sleeps, `networkidle`, and assertions on implementation internals.
- Keep tests independent. Playwright creates a new browser context per test. The current Go host is shared and stateless; add isolated temporary application data per worker before testing persistent state.
- Create required state through fixtures once a feature needs it. Do not depend on test execution order or reuse personal accounts, API keys, history, or downloaded models.
- Use controlled local provider fixtures for repeatable failure and retry journeys. Keep live-provider checks separate and explicitly opted into.
- Add real behavior coverage as features ship. Do not add snapshots of static markup, skipped placeholder scenarios, or unnecessary page-object abstractions.

## Failures and CI

Each failed test retains a trace, screenshot, and video under `app/frontend/test-results/`. The HTML report is under `app/frontend/playwright-report/`; browser errors and accessibility results are attached. These directories are ignored by Git. Treat artifacts as potentially sensitive when future tests exercise user content.

CI rejects focused tests and flaky passes. One retry captures additional diagnostics, but a test that only passes on retry still fails the job. Reports and failure artifacts are uploaded for 14 days. `pnpm check` remains the fast lint/type/Go gate; CI runs E2E separately after the native build. No browser downloads or E2E runs are added to the pre-commit hook.
