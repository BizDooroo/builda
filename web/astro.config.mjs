import { defineConfig } from "astro/config";

function entryFileName(chunk) {
  const id = String(chunk.facadeModuleId || "").replaceAll("\\", "/");
  if (id.includes("/src/pages/config/")) return "_astro/config.js";
  if (id.includes("/src/pages/runs/")) return "_astro/runs.js";
  if (id.includes("/src/pages/run.")) return "_astro/run-detail.js";
  if (id.includes("/src/pages/index.")) return "_astro/home.js";
  return "_astro/[name].js";
}

function deterministicClientBuildNames() {
  return {
    name: "builda-deterministic-client-build-names",
    config(_config, env) {
      if (env.isSsrBuild) return {};
      return {
        build: {
          rollupOptions: {
            output: {
              chunkFileNames: "_astro/[name].js",
              entryFileNames: entryFileName,
              minifyInternalExports: false,
            },
          },
        },
      };
    },
  };
}

export default defineConfig({
  output: "static",
  vite: {
    plugins: [deterministicClientBuildNames()],
  },
});
