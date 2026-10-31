import { expect, test } from "@playwright/test";
import katex from "katex";
import { installFakeAgent } from "./support/fake-bridge";

test("KaTeX rejects inherited trust settings @T0", () => {
	const options = Object.assign(Object.create({ trust: true }), { output: "html", strict: "ignore" });
	const html = katex.renderToString(String.raw`\href{javascript:alert(1)}{unsafe}`, options);
	expect(html).not.toContain('href="javascript:');
});

// Exercise the real Mermaid/KaTeX/DOMPurify stack; unit tests mock SVG layout.
// ponytail: Remove the KaTeX override when all consumers accept the patched version.
test("patched diagram dependencies preserve math and sanitize labels @T0", async ({ page }) => {
	await installFakeAgent(page);
	await page.route("http://127.0.0.1:8080/api/v1/**", (route) => route.fulfill({ json: { agents: [] } }));
	await page.goto("/");
	const results = await page.evaluate(async () => {
		const modulePath = "/src/renderer/lib/mermaid-diagram.ts";
		const { renderMermaidDiagram } = await import(/* @vite-ignore */ modulePath);
		const results = [];
		for (const theme of ["light", "dark"]) {
			const svg = await renderMermaidDiagram('flowchart TD\nA["$$x^2 + y^2$$"] --> B["Safe label"]', theme);
			const container = document.createElement("div");
			container.innerHTML = svg;
			results.push({
				svg: container.querySelector("svg") !== null,
				math: container.querySelector(".katex") !== null,
				label: container.textContent?.includes("Safe label"),
			});
		}
		const svg = await renderMermaidDiagram('flowchart TD\nA["<img src=x onerror=alert(1)>Safe label"]', "light");
		const container = document.createElement("div");
		container.innerHTML = svg;
		return {
			rendered: results,
			unsafeElements: container.querySelectorAll("script, img, image, iframe, [onerror], [onload]").length,
			safeLabel: container.textContent?.includes("Safe label"),
		};
	});
	expect(results.rendered).toEqual([
		{ svg: true, math: true, label: true },
		{ svg: true, math: true, label: true },
	]);
	expect(results.unsafeElements).toBe(0);
	expect(results.safeLabel).toBe(true);
});
