import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { test } from "node:test";

const original = [
	"clearTimeout(closeTimerRef.current);\n    openTimerRef.current =",
	"clearTimeout(openTimerRef.current);\n    if (!hasSelectionRef.current",
];
const patched = [
	"clearTimeout(closeTimerRef.current);\n    clearTimeout(openTimerRef.current);\n    openTimerRef.current =",
	"clearTimeout(openTimerRef.current);\n    clearTimeout(closeTimerRef.current);\n    if (!hasSelectionRef.current",
];

function fixture(t, esm, cjs = esm, version = "1.1.16") {
	const root = mkdtempSync(join(tmpdir(), "ao-hover-card-patch-"));
	t.after(() => rmSync(root, { recursive: true, force: true }));
	const packageRoot = join(root, "node_modules", "@radix-ui", "react-hover-card");
	mkdirSync(join(packageRoot, "dist"), { recursive: true });
	writeFileSync(join(packageRoot, "package.json"), JSON.stringify({ version, main: "dist/index.js" }));
	const files = [join(packageRoot, "dist", "index.mjs"), join(packageRoot, "dist", "index.js")];
	files.forEach((path, index) => writeFileSync(path, [esm, cjs][index]));
	const script = join(root, "patch-hover-card.mjs");
	copyFileSync(new URL("./patch-hover-card.mjs", import.meta.url), script);
	return { files, run: () => spawnSync(process.execPath, [script], { encoding: "utf8" }) };
}

test("patches ESM and CJS once and preserves bytes on repeated installation", t => {
	const { files, run } = fixture(t, original.join("\n"));
	assert.equal(run().status, 0);
	const expected = patched.join("\n");
	files.forEach(path => assert.equal(readFileSync(path, "utf8"), expected));
	assert.equal(run().status, 0);
	files.forEach(path => assert.equal(readFileSync(path, "utf8"), expected));
});

for (const shape of ["mixed", "duplicate original", "duplicate patched", "missing"]) {
	for (const block of [0, 1]) {
		test(`refuses ${shape} timer block ${block} before either file is written`, t => {
			const blocks = [...patched];
			blocks[block] = shape === "mixed" ? patched[block] + "\n" + original[block]
				: shape === "duplicate original" ? original[block] + "\n" + original[block]
					: shape === "duplicate patched" ? patched[block] + "\n" + patched[block] : "";
			const cjs = blocks.join("\n");
			const esm = original.join("\n");
			const { files, run } = fixture(t, esm, cjs);
			const result = run();
			assert.equal(result.status, 1);
			assert.match(result.stderr, /HoverCard timer shape changed/);
			assert.equal(readFileSync(files[0], "utf8"), esm);
			assert.equal(readFileSync(files[1], "utf8"), cjs);
		});
	}
}

test("refuses an unverified dependency version before writing", t => {
	const source = original.join("\n");
	const { files, run } = fixture(t, source, source, "1.1.17");
	const result = run();
	assert.equal(result.status, 1);
	assert.match(result.stderr, /Recheck the HoverCard timer patch/);
	files.forEach(path => assert.equal(readFileSync(path, "utf8"), source));
});
