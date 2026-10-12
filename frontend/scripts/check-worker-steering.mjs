import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import * as helpers from "./build-acp-runtime-helpers.mjs";

const directory = mkdtempSync(join(tmpdir(), "ao-worker-steering-"));
try {
	assert.equal(typeof helpers.patchClaudeWorkerSteering, "function", "worker steering runtime patch is missing");
	const adapter = join(directory, "acp-agent.js");
	const parse = `function parseSteerRequest(params) {
    const { sessionId, prompt, _meta } = params;
    const steering = _meta && typeof _meta === "object" ? _meta.steering : undefined;
    const idleBehavior = steering && typeof steering === "object" ? steering.idleBehavior : undefined;
    if (idleBehavior !== undefined && idleBehavior !== "promptRequired") {
        throw RequestError.invalidParams(undefined, "unsupported steering idleBehavior");
    }
    return {
        sessionId,
        prompt: prompt,
        _meta: _meta,
    };
}`;
	const fixture = `${parse}
const initialized = { steering: {
                    supported: true,
                } };
const agent = { async steer(params) {
 const userMessage = { message: { role: "user", content: params.prompt } };
 userMessage.priority = STEER_PRIORITY;
 session.input.push(userMessage);
 return { outcome: "injected" };
} };
return { parseSteerRequest, agent, initialized };`;
	writeFileSync(adapter, fixture);
	assert.equal(helpers.patchClaudeWorkerSteering(adapter), true);
	assert.equal(helpers.patchClaudeWorkerSteering(adapter), false);
	const pushed = [];
	const session = { input: { push: message => pushed.push(message) } };
	const { parseSteerRequest, agent, initialized } = new Function("session", "STEER_PRIORITY", "RequestError", readFileSync(adapter, "utf8"))(
		session, "now", { invalidParams: (_, message) => new Error(message) },
	);
	assert.equal(initialized.steering.aoWorkerSafeBoundary, true);
	await agent.steer(parseSteerRequest({ sessionId: "test", prompt: "worker update", _meta: { steering: { priority: "next", senderSessionId: "worker-54" } } }));
	assert.equal(pushed[0].priority, "next");
	assert.deepEqual(pushed[0].origin, { kind: "peer", from: "worker-54" });
	await agent.steer(parseSteerRequest({ sessionId: "test", prompt: "explicit human correction" }));
	assert.equal(pushed[1].priority, "now");
	assert.equal(pushed[1].origin, undefined);
	for (const steering of [
		{ priority: "next" },
		{ priority: "later", senderSessionId: "worker" },
		{ senderSessionId: "worker" },
		{ priority: "next", senderSessionId: "" },
	]) {
		assert.throws(() => parseSteerRequest({ sessionId: "test", prompt: "invalid", _meta: { steering } }), /worker steering/);
	}
	writeFileSync(adapter, "// incompatible pinned adapter");
	assert.throws(() => helpers.patchClaudeWorkerSteering(adapter), /worker steering/);
	console.log("PASS: worker delivery uses peer/next; human steering unchanged; malformed metadata and incompatible adapter refused");
} finally {
	rmSync(directory, { recursive: true, force: true });
}
