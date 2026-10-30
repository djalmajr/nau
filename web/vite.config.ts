import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

// Dev: Vite serve o SPA com HMR e faz proxy de /api -> hub (:7440, porta
// padrão do hub), então o front roda same-origin sem CORS.
// 127.0.0.1 (não "localhost"): o hub binda 127.0.0.1 (IPv4); "localhost"
// poderia resolver para ::1 (IPv6) e o proxy falharia (ECONNREFUSED).
const apiProxyTarget = process.env.VITE_API_PROXY_TARGET ?? "http://127.0.0.1:7440";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "./src") },
  },
  server: {
    // 5182 (não 5181) para coexistir com outros dev-servers locais.
    port: 5182,
    strictPort: true,
    proxy: {
      "/api": { target: apiProxyTarget, changeOrigin: true },
    },
  },
  build: {
    // O hub vai servir web/dist com fallback para index.html (card seguinte).
    outDir: "dist",
    emptyOutDir: true,
  },
});
