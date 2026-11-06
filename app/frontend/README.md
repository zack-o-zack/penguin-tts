# Penguin UI

React 19 and Vite 8 render the Wails desktop frontend. The UI uses Tailwind CSS v4 and shadcn/ui's Radix Nova registry, initialized with shadcn 4.23.0. All 63 UI components from `shadcn add --all` are checked into `src/components/ui/`, with supporting hooks and highlighting helpers.

Import components directly so unused JavaScript can be removed from the production bundle:

```tsx
import { Button } from "@/components/ui/button";

<Button>Generate speech</Button>;
```

`@/` resolves to `src/` in both TypeScript and Vite. `components.json` records the registry style and import locations. From the monorepo root, add or inspect upstream components with the pinned CLI:

```sh
pnpm --filter @penguin/frontend exec shadcn add button --diff
```

The copied registry components and helpers retain the [upstream MIT notice](src/components/ui/LICENSE).

Review upstream updates before overwriting local accessibility and strict TypeScript fixes. Registry wrappers have file-scoped aislop exceptions for repeated wrapper structure and the supplied sidebar's size. Biome, TypeScript, security checks, and the root score threshold remain enabled.

## Brand theme

`src/styles.css` defines Penguin's semantic color tokens. Dark mode uses charcoal (`#111419`), mint actions (`#b4e4c5`), and light text (`#edf1f5`). The companion light palette uses deep green actions. The font remains the system font; no external font requests are needed. `index.html` selects dark mode before React starts.

Use semantic utilities such as `bg-primary`, `text-primary-foreground`, and `border-input`. Tokens also cover cards, overlays, sidebars, validation, and five chart colors. Charts still need labels or patterns and a text/table alternative for their data.

Tailwind v4 requires Safari 16.4 or newer. The native build targets and both macOS bundles therefore require macOS 13.3 or newer.

## Accessibility

The theme includes a solid keyboard-focus outline, control borders with visible contrast, and a reduced-motion override. Components preserve primitive keyboard behavior. Consumers must still supply meaningful labels for inputs, icon actions, dialogs, sliders, and grouped controls. When adapting a range slider, give each thumb a distinct, meaningful accessible name.

Run `pnpm test:e2e` from the monorepo root. In addition to startup and narrow-window coverage, the suite checks:

- axe WCAG A/AA rules through WCAG 2.2 against representative components in both palettes.
- Checkbox, switch, select, slider, tab, and accordion keyboard interactions.
- Dialog initial focus, focus trapping, Escape, and focus restoration.
- Focus outlines and reduced motion in Chromium and WebKit.

`e2e/components.html` is a test fixture. Vite includes it only in `--mode e2e` builds; normal desktop builds exclude it. Test reports retain the raw axe results.

These checks cover the shell and tested component compositions. They do not certify every component or replace VoiceOver testing in the native Wails window, testing on the minimum supported macOS version, or an audit of future product workflows.
