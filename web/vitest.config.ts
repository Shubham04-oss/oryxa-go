import { defineConfig } from "vitest/config";

export default defineConfig({
	// The app's own aliases, so a test can import a component as the app does.
	resolve: { tsconfigPaths: true },
	test: {
		include: ["src/**/*.test.{ts,tsx}"],
	},
});
