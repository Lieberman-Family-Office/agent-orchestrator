import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";

// ponytail: Patch Radix 1.1.16 only; remove after upgrading to a verified timer fix.
const require = createRequire(import.meta.url);
const directory = dirname(require.resolve("@radix-ui/react-hover-card"));
const metadata = JSON.parse(readFileSync(join(directory, "..", "package.json"), "utf8"));
assert.equal(metadata.version, "1.1.16", "Recheck the HoverCard timer patch after a dependency change.");
const replacements = [
	["clearTimeout(closeTimerRef.current);\n    openTimerRef.current =", "clearTimeout(closeTimerRef.current);\n    clearTimeout(openTimerRef.current);\n    openTimerRef.current ="],
	["clearTimeout(openTimerRef.current);\n    if (!hasSelectionRef.current", "clearTimeout(openTimerRef.current);\n    clearTimeout(closeTimerRef.current);\n    if (!hasSelectionRef.current"],
];
const files = ["index.mjs", "index.js"].map(name => {
	const path = join(directory, name);
	let source = readFileSync(path, "utf8");
	for (const [original, patched] of replacements) {
		if (source.split(patched).length === 2) continue;
		assert.equal(source.split(original).length, 2, `HoverCard timer shape changed in ${name}.`);
		source = source.replace(original, patched);
	}
	return { path, source };
});
for (const { path, source } of files) writeFileSync(path, source);
