# Repository conventions

- Run commands from the monorepo root unless working on the Go module in `app/`.
- Use pnpm for JavaScript dependencies. Keep one workspace lockfile and exact dependency versions.
- Keep root Git hooks and aislop configuration shared by every workspace. Do not add nested hook installations.
- Run `pnpm check` and `pnpm build` for changes to the desktop shell or build tooling. Run narrower checks when a change only affects documentation.
- Do not commit generated bindings, frontend assets, binaries, dependency directories, API keys, or `.env` files.
- Keep product requirements and research in `.spikes/`.
