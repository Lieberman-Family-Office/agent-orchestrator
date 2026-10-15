import { defineConfig } from "vite";

// better-sqlite3 is a native module rebuilt for Electron by Forge. Keep it out
// of Vite's bundle so the packaged app loads the rebuilt binary from
// node_modules (the auto-unpack plugin moves that binary outside app.asar).
export default defineConfig({
	define: {
		"process.env.AO_MANUAL_UPDATES": JSON.stringify(process.env.AO_MANUAL_UPDATES === "true" ? "true" : "false"),
	},
	build: {
		rollupOptions: {
			external: ["better-sqlite3"],
		},
	},
});
