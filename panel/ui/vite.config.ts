import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      "/api": "http://127.0.0.1:10808",
      "/login": "http://127.0.0.1:10808",
      "/logout": "http://127.0.0.1:10808"
    }
  },
  build: {
    outDir: "dist",
    emptyOutDir: true
  }
});
