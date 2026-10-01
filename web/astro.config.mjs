import { defineConfig } from "astro/config";

const pageEntryNames = [
  ["/src/pages/login/", "_astro/login.js"],
  ["/src/pages/catalogs/", "_astro/catalogs.js"],
  ["/src/pages/agents/", "_astro/agents.js"],
  ["/src/pages/queue/", "_astro/queue.js"],
  ["/src/pages/settings/", "_astro/settings.js"],
  ["/src/pages/runs/", "_astro/runs.js"],
  ["/src/pages/run.", "_astro/run-detail.js"],
  ["/src/pages/index.", "_astro/jobs.js"],
];

function entryFileName(chunk) {
  const id = String(chunk.facadeModuleId || "").replaceAll("\\", "/");
  for (const [needle, name] of pageEntryNames) {
    if (id.includes(needle)) return name;
  }
  return "_astro/[name].js";
}

// Shared modules each get their own chunk. Letting Rollup merge them into one
// chunk makes the generated export order vary between builds, which breaks the
// CI comparison against the committed dist.
const sharedModules = new Set([
  "api",
  "clipboard",
  "format",
  "forms",
  "i18n",
  "logview",
  "prefs",
  "shell",
]);

function sharedChunkName(id) {
  const match = String(id).replaceAll("\\", "/").match(/\/src\/scripts\/([a-z0-9-]+)\.js$/);
  if (!match) return undefined;
  return sharedModules.has(match[1]) ? match[1] : undefined;
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
              manualChunks: sharedChunkName,
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
