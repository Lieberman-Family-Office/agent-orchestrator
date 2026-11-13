import { defineConfig } from "vite";
import { sentrySourceMaps } from "./vite.sentry";

const sentry = sentrySourceMaps();

// better-sqlite3 is a native module rebuilt for Electron by Forge. Keep it out
// of Vite's bundle so the packaged app loads the rebuilt binary from
// node_modules (the auto-unpack plugin moves that binary outside app.asar).
export default defineConfig({
	// Only published builds initialize Sentry; private builds retain manual updates.
	define: {
		"process.env.AO_MANUAL_UPDATES": JSON.stringify(process.env.AO_MANUAL_UPDATES === "true" ? "true" : "false"),
		"process.env.AO_PUBLISHED_BUILD": JSON.stringify(process.env.AO_PUBLISHED_BUILD === "1" ? "1" : ""),
	},
	plugins: sentry.plugins,
	build: {
		sourcemap: sentry.sourcemap,
		rollupOptions: {
			external: ["better-sqlite3"],
		},
	},
});
