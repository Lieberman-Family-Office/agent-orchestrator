const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

const source = fs.readFileSync(path.join(__dirname, "browser"), "utf8");

test("remote browser files stay inside the working directory", async (t) => {
	const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "browser-files-")));
	const previousCwd = process.cwd();
	t.after(() => {
		process.chdir(previousCwd);
		fs.rmSync(root, { recursive: true, force: true });
	});
	const cwd = path.join(root, "work");
	fs.mkdirSync(cwd);
	process.chdir(cwd);
	const outside = path.join(root, "outside");
	fs.mkdirSync(outside);
	const sentinel = path.join(outside, "sentinel");
	fs.writeFileSync(sentinel, "unchanged");
	fs.symlinkSync(outside, path.join(cwd, "linked-dir"), "dir");
	fs.symlinkSync(sentinel, path.join(cwd, "linked-file"), "file");
	fs.symlinkSync(path.join(outside, "missing"), path.join(cwd, "dangling"), "file");
	fs.linkSync(sentinel, path.join(cwd, "hard-link"));
	const run = async (name, data = Buffer.from("image").toString("base64")) => {
		let status = 0;
		await vm.runInNewContext(source, {
			require, Buffer,
			process: { argv: ["node", "browser", "screenshot"], env: {}, cwd: () => cwd,
				stdout: { write() {} }, exit(code) { status = code; } },
			console: { error() {} },
			fetch: async () => ({ json: async () => ({ files: { [name]: data } }) }),
		});
		return status;
	};
	for (const name of ["shot.png", "nested/shot.png", "..reports/shot.png"]) {
		assert.equal(await run(name), 0, name);
		assert.equal(fs.readFileSync(path.join(cwd, name), "utf8"), "image");
	}
	fs.writeFileSync(path.join(cwd, "shot.png"), "longer previous image");
	assert.equal(await run("shot.png"), 0);
	assert.equal(fs.readFileSync(path.join(cwd, "shot.png"), "utf8"), "image");
	for (const name of ["", ".", "../outside/sentinel", sentinel, "nested/../../outside/sentinel",
		"../work-sibling/new", "linked-dir/sentinel", "linked-dir/new/shot.png", "linked-file", "dangling", "hard-link"]) {
		assert.equal(await run(name), 1, `must reject ${JSON.stringify(name)}`);
		assert.equal(fs.readFileSync(sentinel, "utf8"), "unchanged", name);
	}
	for (const data of [null, 42, {}, "not base64!"]) {
		assert.equal(await run("shot.png", data), 1);
		assert.equal(fs.readFileSync(path.join(cwd, "shot.png"), "utf8"), "image");
	}
	assert.deepEqual(fs.readdirSync(outside), ["sentinel"]);
	assert.equal(fs.existsSync(path.join(root, "work-sibling")), false);
});
