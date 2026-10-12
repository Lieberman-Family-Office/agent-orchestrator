import assert from "node:assert/strict";
import { cpSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { execFileSync } from "node:child_process";
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
async function handleAskUserQuestion(sessionId, toolInput, toolUseID, signal) {
        const questions = extractAskUserQuestions(toolInput);
        if (!questions) {
            return { behavior: "deny", message: "AskUserQuestion called with no valid questions." };
        }
        const createRequest = askUserQuestionsToCreateRequest(questions, sessionId, toolUseID);
        let response;
        try {
            response = await this.client.unstable_createElicitation(createRequest, signal);
        }
        catch (error) {
            if (signal.aborted) throw new Error("Tool use aborted");
            return { behavior: "deny", message: "Could not present the question to the user." };
        }
        if (signal.aborted) {
            throw new Error("Tool use aborted");
        }
        const outcome = applyAskElicitationResponse(response, toolInput, questions);
        if (outcome.action === "cancel") {
            throw new Error("Tool use aborted");
        }
        return { behavior: "allow", updatedInput: outcome.updatedInput };
}
function commandLifecycle(message) {
                    const frame = message;
                    switch (frame.state) {
                        case "started": break;
                    }
}
const initialized = { steering: {
                    supported: true,
                } };
const agent = { async steer(params) {
 const steeredUuid = "worker-message";
 const userMessage = { message: { role: "user", content: [{ type: "text", text: params.prompt }] } };
 userMessage.priority = STEER_PRIORITY;
 session.input.push(userMessage);
 return { outcome: "injected" };
} };
return { parseSteerRequest, agent, initialized, handleAskUserQuestion, commandLifecycle };`;
	writeFileSync(adapter, fixture);
	assert.equal(helpers.patchClaudeWorkerSteering(adapter), true);
	assert.equal(helpers.patchClaudeWorkerSteering(adapter), false);
	const pushed = [];
	const session = { input: { push: message => pushed.push(message) } };
	const { parseSteerRequest, agent, initialized, handleAskUserQuestion, commandLifecycle } = new Function("session", "STEER_PRIORITY", "RequestError", "extractAskUserQuestions", "askUserQuestionsToCreateRequest", "applyAskElicitationResponse", readFileSync(adapter, "utf8"))(
		session, "now", { invalidParams: (_, message) => new Error(message) }, () => [{}], () => ({}), response => ({ action: response.action, updatedInput: response.content }),
	);
	const sdkSignal = new AbortController();
	const elicitation = { sessions: { test: session }, client: { unstable_createElicitation: async request => {
		assert.equal(request._meta.aoWorkerQuestionYield, true);
		return { action: "cancel", _meta: { aoWorkerQuestionYield: "automatic-worker-deferral" } };
	} } };
	const deferred = await handleAskUserQuestion.call(elicitation, "test", {}, "question", sdkSignal.signal);
	assert.equal(deferred.behavior, "deny");
	assert.equal(deferred.interrupt, false);
	assert.match(deferred.message, /automatic|automatically/i);
	assert.equal(sdkSignal.signal.aborted, false);
	sdkSignal.abort();
	await assert.rejects(handleAskUserQuestion.call(elicitation, "test", {}, "question", sdkSignal.signal), /Tool use aborted/);
	assert.equal(initialized.steering.aoWorkerSafeBoundary, true);
	await agent.steer(parseSteerRequest({ sessionId: "test", prompt: "worker update", _meta: { steering: { priority: "next", senderSessionId: "worker-54" } } }));
	assert.equal(pushed[0].priority, "next");
	assert.deepEqual(pushed[0].origin, { kind: "peer", from: "worker-54" });
	assert.equal(pushed[0].message.content[0].text, '<cross-session-message from="worker-54">\nworker update\n</cross-session-message>');
	assert.equal(session.aoWorkerSteeringEpoch, 1);
	assert.equal(session.aoWorkerSteering.size, 1);
	session.turnQueue = [{ steeredEchoes: new Set(["worker-message", "human-message"]) }];
	commandLifecycle({ command_uuid: "worker-message", state: "started" });
	assert.equal(session.aoWorkerSteering.size, 0);
	assert.deepEqual([...session.turnQueue[0].steeredEchoes], ["human-message"]);
	commandLifecycle({ command_uuid: "human-message", state: "started" });
	assert.deepEqual([...session.turnQueue[0].steeredEchoes], ["human-message"]);
	await agent.steer(parseSteerRequest({ sessionId: "test", prompt: "explicit human correction" }));
	assert.equal(pushed[1].priority, "now");
	assert.equal(pushed[1].origin, undefined);
	for (const steering of [
		{ priority: "next" },
		{ priority: "later", senderSessionId: "worker" },
		{ senderSessionId: "worker" },
		{ priority: "next", senderSessionId: "" },
		{ priority: "next", senderSessionId: 'worker" injected="true' },
	]) {
		assert.throws(() => parseSteerRequest({ sessionId: "test", prompt: "invalid", _meta: { steering } }), /worker steering/);
	}
	writeFileSync(adapter, "// incompatible pinned adapter");
	assert.throws(() => helpers.patchClaudeWorkerSteering(adapter), /worker steering/);
	const nativeCLI = process.argv[3];
	if (nativeCLI) {
		const native = execFileSync("strings", [nativeCLI], { encoding: "utf8", maxBuffer: 256 * 1024 * 1024 });
		const parser = native.match(/function xht\(e\)\{if\(typeof e!=="string"\)return;return e\.match\(cNn\)\?\.\[1\]\}/)?.[0];
		const peer = native.match(/function oC\(e,r\)\{return e\?e\.kind==="peer"\?e\.from:void 0:xht\(r\)\}/)?.[0];
		const priority = native.match(/function IGo\(e,n\)\{if\(e==="now"\)return e;return n\?"later":e\}/)?.[0];
		const regex = native.match(/cNn=new RegExp\(`\^<\$\{fz\} from="\(\[\^"\]\+\)"`\)/)?.[0];
		assert.ok(parser && peer && priority && regex && native.includes('fz="cross-session-message"'), "native ingress changed; attribution check cannot run");
		const ingress = new Function(`const fz="cross-session-message"; const ${regex}; ${parser}; ${peer}; ${priority}; return { peer:oC, priority:IGo };`)();
		assert.equal(ingress.peer(undefined, pushed[0].message.content[0].text), "worker-54");
		assert.equal(ingress.peer(undefined, "plain worker update"), undefined);
		assert.equal(ingress.priority("next", false), "next");
		assert.ok(native.includes('getCommandsByMaxPriority("next")') && native.includes('reason:"absorbed_mid_turn"'), "native safe-boundary scheduler changed");
		console.log("PASS: extracted native CLI ingress recognizes the actual worker envelope and retains next priority");
	}
	const nativeAdapter = process.argv[2];
	if (nativeAdapter) {
		// Copy the adapter; dependencies are read-only. Never patch the installed app.
		const runtime = join(directory, "runtime");
		cpSync(dirname(dirname(nativeAdapter)), runtime, { recursive: true });
		symlinkSync(dirname(dirname(dirname(dirname(nativeAdapter)))), join(directory, "node_modules"));
		const copy = join(runtime, "dist", "acp-agent.js");
		try {
			helpers.patchClaudeWorkerSteering(copy);
			execFileSync(process.execPath, ["--check", copy]);
			const { ClaudeAcpAgent } = await import(pathToFileURL(copy).href);
			const prototype = ClaudeAcpAgent.prototype;
			const input = { questions: [{ question: "Select scope?", header: "Scope", options: [{ label: "Narrow", description: "One item" }, { label: "Broad", description: "All items" }], multiSelect: false }] };
			const responseCases = [
				[{ action: "cancel", _meta: { aoWorkerQuestionYield: "automatic-worker-deferral" } }, false, "deny"],
				[{ action: "accept", content: { question_0: "Narrow" } }, false, "allow"],
				[{ action: "cancel" }, false, "aborted"],
				[{ action: "cancel", _meta: { aoWorkerQuestionYield: "automatic-worker-deferral" } }, true, "aborted"],
			];
			for (const [response, stop, expected] of responseCases) {
				const signal = new AbortController();
				const bridge = { sessions: { test: {} }, logger: { error() {} }, client: { unstable_createElicitation: async request => {
					assert.equal(request._meta.aoWorkerQuestionYield, true);
					if (stop) signal.abort();
					return response;
				} } };
				const pending = prototype.handleAskUserQuestion.call(bridge, "test", input, "question", signal.signal);
				if (expected === "aborted") await assert.rejects(pending, /Tool use aborted/);
				else {
					const outcome = await pending;
					assert.equal(outcome.behavior, expected);
					if (expected === "deny") assert.equal(outcome.interrupt, false);
					assert.equal(signal.signal.aborted, false);
				}
			}
			let complete;
			const signal = new AbortController();
			const realPushed = [];
			const turn = { settled: false };
			const nativeSession = { turnQueue: [turn], activeTurn: turn, input: { push: message => realPushed.push(message) } };
			const bridge = { sessions: { test: nativeSession }, publishGoalFromPrompt: async () => {}, client: { unstable_createElicitation: () => new Promise(resolve => { complete = resolve; }) } };
			const pending = prototype.handleAskUserQuestion.call(bridge, "test", input, "question", signal.signal);
			const outcome = await prototype.steer.call(bridge, { sessionId: "test", prompt: [{ type: "text", text: "worker update" }], _meta: { steering: { priority: "next", senderSessionId: "worker-54", idleBehavior: "promptRequired" } } });
			assert.equal(outcome.outcome, "injected");
			assert.equal(realPushed[0].priority, "next");
			assert.match(realPushed[0].message.content[0].text, /^<cross-session-message from="worker-54">\n/);
			assert.equal(signal.signal.aborted, false);
			complete({ action: "cancel", _meta: { aoWorkerQuestionYield: "automatic-worker-deferral" } });
			assert.equal((await pending).interrupt, false);
			console.log("PASS: real pinned bridge releases only the question callback; explicit cancellation and human answers retain semantics");
		} finally {
			rmSync(copy, { force: true });
		}
	}
	console.log("PASS: worker envelope/next, typed automatic deferral, real-stop precedence, human steering and metadata validation");
} finally {
	rmSync(directory, { recursive: true, force: true });
}
