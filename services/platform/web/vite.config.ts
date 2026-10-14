import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    host: "127.0.0.1",
    port: 5192,
    strictPort: true,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8092",
        changeOrigin: true,
        configure(proxy) {
          proxy.on("proxyReq", (req, incoming) => {
            if (incoming.headers.origin === "http://127.0.0.1:5192")
              req.setHeader("Origin", "http://127.0.0.1:8092");
          });
        },
      },
      "/healthz": "http://127.0.0.1:8092",
    },
  },
});
