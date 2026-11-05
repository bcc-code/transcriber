import tailwindcss from "@tailwindcss/vite";

const TRANSCRIBER_API =
  process.env.NUXT_TRANSCRIBER_API ?? "http://localhost:8888";

export default defineNuxtConfig({
  compatibilityDate: "2025-07-15",
  devtools: { enabled: true },
  ssr: false,
  css: ["~/assets/css/main.css"],
  modules: ["@nuxt/icon", "@nuxt/fonts"],
  components: [{ path: "~/components", pathPrefix: false }],
  vite: {
    plugins: [tailwindcss()],
  },
  app: {
    head: {
      title: "Transcriber",
      htmlAttrs: {
        "data-theme": "dark",
      },
      link: [
        { rel: "icon", type: "image/svg+xml", href: "/favicon.svg" },
        { rel: "alternate icon", type: "image/x-icon", href: "/favicon.ico" },
      ],
    },
  },
  experimental: {
    typedPages: true,
    viewTransition: true,
  },
  // Dev: proxy API paths to the Go server. Prod: the binary serves both, so these resolve natively.
  routeRules: {
    "/transcription/**": { proxy: `${TRANSCRIBER_API}/transcription/**` },
    "/models": { proxy: `${TRANSCRIBER_API}/models` },
    "/models/**": { proxy: `${TRANSCRIBER_API}/models/**` },
    "/config": { proxy: `${TRANSCRIBER_API}/config` },
    "/stats": { proxy: `${TRANSCRIBER_API}/stats` },
    "/healthz": { proxy: `${TRANSCRIBER_API}/healthz` },
    "/readyz": { proxy: `${TRANSCRIBER_API}/readyz` },
  },
});
