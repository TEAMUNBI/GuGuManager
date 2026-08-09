import { defineConfig, type ProxyOptions } from "vite";
import react from "@vitejs/plugin-react";

const DEV_PROXY_ERROR_HEADER = "X-GuGuManager-Proxy-Error";
const DEV_PROXY_UPSTREAM_UNAVAILABLE = "upstream-unavailable";

interface ProxyErrorEmitter {
  on(event: "error", listener: (error: unknown, request: unknown, response: unknown) => void): void;
}

function canMarkProxyError(response: unknown): response is {
  headersSent: boolean;
  setHeader(name: string, value: string): void;
} {
  return typeof response === "object" && response !== null &&
    "headersSent" in response && "setHeader" in response &&
    typeof response.setHeader === "function";
}

function backendProxy(): ProxyOptions {
  return {
    target: "http://127.0.0.1:8080",
    changeOrigin: true,
    configure(proxy) {
      (proxy as unknown as ProxyErrorEmitter).on("error", (_error, _request, response) => {
        if (canMarkProxyError(response) && !response.headersSent) {
          response.setHeader(DEV_PROXY_ERROR_HEADER, DEV_PROXY_UPSTREAM_UNAVAILABLE);
        }
      });
    },
  };
}

export default defineConfig({
  plugins: [react()],
  server: {
    fs: { allow: [".", "../spec"] },
    host: "127.0.0.1",
    port: 4173,
    proxy: {
      "/api": backendProxy(),
      "/healthz": backendProxy(),
    },
  },
});
