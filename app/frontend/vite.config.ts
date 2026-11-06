import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import wails from "@wailsio/runtime/plugins/vite";
import { defineConfig } from "vite";

export default defineConfig(({ mode }) => ({
  build: {
    rollupOptions: {
      input:
        mode === "e2e" ? ["index.html", "e2e/components.html"] : "index.html",
    },
  },
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    host: "127.0.0.1",
    port: 9245,
    strictPort: true,
  },
  plugins: [react(), tailwindcss(), wails("./bindings")],
}));
