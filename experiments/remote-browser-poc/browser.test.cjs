const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");
const { spawnSync } = require("node:child_process");

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

test("browser service tokens require private owned storage", (t) => {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "browser-token-"));
	t.after(() => fs.rmSync(root, { recursive: true, force: true }));
	const bin = path.join(root, "bin");
	fs.mkdirSync(bin);
	fs.writeFileSync(path.join(bin, "df"), "#!/bin/sh\nprintf 'Filesystem Blocks Used Available Capacity Mounted\\nmock 100 100 0 100%% /\\n'\n", { mode: 0o700 });
	// Mirror the script and relocate the old unsafe path, so RED cannot touch a real token.
	const script = path.join(root, "up.sh");
	fs.writeFileSync(script, fs.readFileSync(path.join(__dirname, "up.sh"), "utf8").replaceAll("/tmp/rbpoc-token", path.join(root, "legacy-token")));
	const run = (data) => spawnSync("bash", [script], {
		env: { ...process.env, HOME: root, AO_DATA_DIR: data, PATH: `${bin}${path.delimiter}${process.env.PATH}` },
		encoding: "utf8", timeout: 5000,
	});
	const data = path.join(root, "data");
	const directory = path.join(data, "remote-browser-poc");
	const token = path.join(directory, "token");
	assert.equal(run(data).status, 3, "valid storage reaches the mocked disk-space guard");
	assert.equal(fs.statSync(directory).mode & 0o777, 0o700);
	assert.equal(fs.statSync(token).mode & 0o777, 0o600);
	const original = fs.readFileSync(token, "utf8");
	assert.match(original, /^[0-9a-f]{48}\n$/);
	assert.equal(run(data).status, 3);
	assert.equal(fs.readFileSync(token, "utf8"), original);
	for (const kind of ["directory-link", "public-directory", "file-link", "hard-link", "public-file", "empty-file", "malformed-file"]) {
		const base = path.join(root, kind);
		const dir = path.join(base, "remote-browser-poc");
		const file = path.join(dir, "token");
		fs.mkdirSync(base);
		if (kind === "directory-link") {
			fs.symlinkSync(directory, dir, "dir");
		} else {
			fs.mkdirSync(dir, { mode: kind === "public-directory" ? 0o755 : 0o700 });
			if (kind === "file-link") fs.symlinkSync(token, file);
			else if (kind === "hard-link") fs.linkSync(token, file);
			else fs.writeFileSync(file, kind === "empty-file" ? "" : kind === "malformed-file" ? "invalid" : original, { mode: kind === "public-file" ? 0o644 : 0o600 });
		}
		assert.equal(run(base).status, 1, `must reject ${kind} before building`);
		assert.equal(fs.readFileSync(token, "utf8"), original);
	}
});
