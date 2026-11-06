export default {
  "*.{js,jsx,mjs,cjs,ts,tsx,json,jsonc,css}":
    "biome check --write --no-errors-on-unmatched",
  "*.{md,yml,yaml}": "prettier --write --ignore-unknown",
  "*.go": "gofmt -w",
};
