import assert from "node:assert/strict";
import { cpSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
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
const questionBridge = { async handleAskUserQuestion(sessionId, toolInput, toolUseID, signal) {
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
}, async dispatch(sessionId, toolInput, toolUseID, signal, agentID, parentToolUseId) {
    return this.handleAskUserQuestion(sessionId, toolInput, toolUseID, signal);
} };
function commandLifecycle(message) {
                    const frame = message;
                    switch (frame.state) {
                        case "started": break;
                    }
}
function initializeMessage(message) {
 switch (message.subtype) {
                            case "init": break;
 }
}
async function activateFixture(turn) {
            session.activeTurn = turn;
}
async function teardownFixture(sessionId) {
        await this.cancel({ sessionId });
}
async function cancelFixture() {
        session.cancelled = true;
 for (const uuid of session.activeTurn?.steeredEchoes ?? []) {
                this.trackOrphanCommand(session, uuid, "pending");
 }
 const receipt = await session.query.interrupt();
 return receipt;
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
return { parseSteerRequest, agent, initialized, handleAskUserQuestion: questionBridge.handleAskUserQuestion, commandLifecycle };`;
	writeFileSync(adapter, fixture);
	assert.equal(helpers.patchClaudeWorkerSteering(adapter), true);
	assert.equal(helpers.patchClaudeWorkerSteering(adapter), false);
	const pushed = [];
	const session = { aoInterruptCancelQueued: true, input: { push: message => pushed.push(message) } };
	const { parseSteerRequest, agent, initialized, handleAskUserQuestion, commandLifecycle } = new Function("session", "STEER_PRIORITY", "RequestError", "extractAskUserQuestions", "askUserQuestionsToCreateRequest", "applyAskElicitationResponse", readFileSync(adapter, "utf8"))(
		session, "now", { invalidParams: (_, message) => new Error(message), internalError: (_, message) => new Error(message) }, () => [{}], () => ({}), response => ({ action: response.action, updatedInput: response.content }),
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
	assert.equal(pushed[1].priority, "next", "ordinary human guidance must not cancel a running tool");
	assert.equal(pushed[1].origin, undefined, "human guidance must not become worker input");
	assert.equal(initialized.steering.aoHumanSafeBoundary, true);
	assert.equal(initialized.steering.aoGuidanceStopFence, 3);
	await agent.steer(parseSteerRequest({ sessionId: "test", prompt: "ledger receipt", _meta: { steering: { priority: "next" } } }));
	assert.equal(pushed[2].priority, "next");
	assert.equal(pushed[2].origin, undefined);
	assert.equal(pushed[2].message.content[0].text, "ledger receipt");
	assert.equal(session.aoWorkerSteering.size, 0, "human guidance must not yield an unanswered form as worker input");
	for (const steering of [
		{ priority: "now" },
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
	const nativeAdapter = process.argv[2] ? resolve(process.argv[2]) : undefined;
	if (nativeAdapter) {
		// Copy the adapter; dependencies are read-only. Never patch the installed app.
		const runtime = join(directory, "runtime");
		cpSync(dirname(dirname(nativeAdapter)), runtime, { recursive: true });
		symlinkSync(dirname(dirname(dirname(dirname(nativeAdapter)))), join(directory, "node_modules"));
		const copy = join(runtime, "dist", "acp-agent.js");
		try {
			helpers.patchClaudeWorkerSteering(copy);
			const sdkPackage = join(runtime, "node_modules", "@anthropic-ai", "claude-agent-sdk");
			cpSync(join(dirname(dirname(dirname(dirname(nativeAdapter)))), "@anthropic-ai", "claude-agent-sdk"), sdkPackage, { recursive: true });
			const sdkCopy = join(sdkPackage, "sdk.mjs");
			assert.equal(helpers.patchClaudeQueuedStop(sdkCopy), true);
			assert.equal(helpers.patchClaudeQueuedStop(sdkCopy), false);
			execFileSync(process.execPath, ["--check", sdkCopy]);
			execFileSync(process.execPath, ["--check", copy]);
			const { ClaudeAcpAgent } = await import(pathToFileURL(copy).href);
			const prototype = ClaudeAcpAgent.prototype;
			const input = { questions: [{ question: "Select scope?", header: "Scope", options: [{ label: "Narrow", description: "One item" }, { label: "Broad", description: "All items" }], multiSelect: false }] };
			let subagentRequest;
			const subagentBridge = { sessions: { test: {} }, client: { unstable_createElicitation: async request => { subagentRequest = request; return { action: "accept", content: { question_0: "Narrow" } }; } } };
			await prototype.handleAskUserQuestion.call(subagentBridge, "test", input, "sub-question", new AbortController().signal, "subagent-7", "parent-task");
			assert.equal(subagentRequest._meta.aoWorkerQuestionYield, false, "subagent question must not yield into a parent-owned continuation");
			assert.equal(subagentRequest._meta.aoQuestionAgentId, "subagent-7");
			assert.equal(subagentRequest._meta.aoQuestionParentToolUseId, "parent-task");
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
			const nativeSession = { aoInterruptCancelQueued: true, turnQueue: [turn], activeTurn: turn, input: { push: message => realPushed.push(message) } };
			const bridge = { sessions: { test: nativeSession }, publishGoalFromPrompt: async () => {}, client: { unstable_createElicitation: () => new Promise(resolve => { complete = resolve; }) } };
			const pending = prototype.handleAskUserQuestion.call(bridge, "test", input, "question", signal.signal);
			const outcome = await prototype.steer.call(bridge, { sessionId: "test", prompt: [{ type: "text", text: "worker update" }], _meta: { steering: { priority: "next", senderSessionId: "worker-54", idleBehavior: "promptRequired" } } });
			assert.equal(outcome.outcome, "injected");
			assert.equal(realPushed[0].priority, "next");
			assert.match(realPushed[0].message.content[0].text, /^<cross-session-message from="worker-54">\n/);
			assert.equal(signal.signal.aborted, false);
			complete({ action: "cancel", _meta: { aoWorkerQuestionYield: "automatic-worker-deferral" } });
			assert.equal((await pending).interrupt, false);
			let answerHumanQuestion;
			const humanSignal = new AbortController();
			const humanPushed = [];
			const humanTurn = { settled: false };
			const humanSession = { aoInterruptCancelQueued: true, turnQueue: [humanTurn], activeTurn: humanTurn, input: { push: message => humanPushed.push(message) } };
			const humanBridge = { sessions: { test: humanSession }, publishGoalFromPrompt: async () => {}, client: { unstable_createElicitation: () => new Promise(resolve => { answerHumanQuestion = resolve; }) } };
			const humanQuestion = prototype.handleAskUserQuestion.call(humanBridge, "test", input, "human-question", humanSignal.signal);
			const humanSteer = await prototype.steer.call(humanBridge, { sessionId: "test", prompt: [{ type: "text", text: "ordinary human guidance" }], _meta: { steering: { priority: "next", idleBehavior: "promptRequired" } } });
			assert.equal(humanSteer.outcome, "injected");
			assert.equal(humanSteer.workerPending, false);
			assert.equal(humanSteer.workerEpoch, 0);
			assert.equal(humanPushed[0].priority, "next");
			assert.deepEqual(humanPushed[0].origin, { kind: "human" });
			assert.equal(humanSignal.signal.aborted, false);
			answerHumanQuestion({ action: "accept", content: { question_0: "Narrow" } });
			assert.equal((await humanQuestion).behavior, "allow");
			const incompatibleSDK = join(directory, "incompatible-sdk.mjs");
			writeFileSync(incompatibleSDK, "export const unsupported = true;");
			assert.throws(() => helpers.patchClaudeQueuedStop(incompatibleSDK), /queued Stop patch/);
			execFileSync(process.execPath, ["--check", sdkCopy]);
			const sdkSource = readFileSync(sdkCopy, "utf8");
			const interruptMethod = sdkSource.match(/async interrupt\([^)]*\)\{[\s\S]*?\}async setPermissionMode/)?.[0].replace(/async setPermissionMode$/, "");
			assert.ok(interruptMethod, "pinned SDK interrupt method changed");
			const interrupt = new Function("lr", `return (${interruptMethod.replace("async interrupt", "async function")});`)((_name, callback) => callback());
			const plainRequests = [];
			await interrupt.call({ request: async request => { plainRequests.push(request); return { response: { still_queued: [] } }; } });
			assert.deepEqual(plainRequests, [{ subtype: "interrupt" }], "unrelated SDK callers must retain plain interrupt semantics");
			for (const lifecycle of [true, false]) {
			const stopRequests = [];
			const waitingGuidance = new Set();
			const locallyQueuedGuidance = [];
			const stopTurn = { settled: false };
			const stopSession = { aoInterruptCancelQueued: true, msgLifecycleV1: lifecycle, turnQueue: [stopTurn], activeTurn: stopTurn, input: { push: message => { message.aoWritten = true; locallyQueuedGuidance.push(message); waitingGuidance.add(message.uuid); } }, query: { interrupt, request: async request => {
				stopRequests.push(request);
				const cancelled = request.cancel_queued === true ? [...waitingGuidance] : [];
				for (const uuid of cancelled) waitingGuidance.delete(uuid);
				return { response: { still_queued: [...waitingGuidance], cancelled } };
			} } };
			const stopBridge = Object.assign(Object.create(prototype), { sessions: { test: stopSession }, publishGoalFromPrompt: async () => {} });
			await prototype.steer.call(stopBridge, { sessionId: "test", prompt: [{ type: "text", text: "pending human guidance" }], _meta: { steering: { priority: "next", idleBehavior: "promptRequired" } } });
			await prototype.steer.call(stopBridge, { sessionId: "test", prompt: [{ type: "text", text: "pending worker guidance" }], _meta: { steering: { priority: "next", senderSessionId: "worker-54", idleBehavior: "promptRequired" } } });
			assert.equal(waitingGuidance.size, 2);
			await prototype.cancel.call(stopBridge, { sessionId: "test" });
			assert.equal(stopRequests.length, 1, "explicit Stop must interrupt immediately");
			assert.equal(stopSession.cancelled, true);
			assert.equal(waitingGuidance.size, 0, "pending human guidance survived Stop and could execute afterward");
			assert.equal(stopRequests[0].cancel_queued, true);
			assert.equal(stopSession.aoPendingGuidance.size, 0);
			assert.ok(locallyQueuedGuidance.every(message => message.aoCancelled === true));
			assert.equal(stopSession.aoWorkerSteering.size, 0);
			assert.equal(stopSession.orphanCommands?.size ?? 0, 0, "cancelled guidance left orphan debt that could swallow the next result");
			assert.equal(stopSession.pendingOrphanResults ?? 0, 0, "count-lane cancellation left phantom result debt");
			await prototype.cancel.call(stopBridge, { sessionId: "test" });
			assert.equal(stopSession.orphanCommands?.size ?? 0, 0, "repeated Stop recreated retired guidance debt");
			assert.equal(stopSession.pendingOrphanResults ?? 0, 0, "repeated Stop recreated count-lane debt");
			const dispatchedMessage = { uuid: "already-dispatched", aoWritten: true };
			const dispatchedTurn = { settled: false, steeredEchoes: new Set([dispatchedMessage.uuid]) };
			const backlogSession = { aoInterruptCancelQueued: true, msgLifecycleV1: lifecycle, turnQueue: [dispatchedTurn], activeTurn: dispatchedTurn, aoPendingGuidance: new Map([[dispatchedMessage.uuid, dispatchedMessage]]), query: { interrupt, request: async () => ({ response: { still_queued: [], cancelled: [] } }) } };
			const backlogBridge = Object.assign(Object.create(prototype), { sessions: { test: backlogSession } });
			await prototype.cancel.call(backlogBridge, { sessionId: "test" });
			if (lifecycle) assert.equal(backlogSession.orphanCommands.get(dispatchedMessage.uuid), "pending", "queue emptiness retired debt without cancellation-fate evidence");
			else assert.equal(backlogSession.pendingOrphanResults, 1, "queue emptiness retired already-dispatched count debt");
			await prototype.cancel.call(backlogBridge, { sessionId: "test" });
			if (lifecycle) assert.equal(backlogSession.orphanCommands.size, 1, "repeated Stop changed dispatch-backlog map debt");
			else assert.equal(backlogSession.pendingOrphanResults, 1, "repeated Stop doubled dispatched-guidance debt");
			const streamSource = sdkSource.match(/async streamInput\(e\)\{[\s\S]*?\}waitForFirstResult\(/)?.[0].replace(/waitForFirstResult\($/, "");
			assert.ok(streamSource, "pinned SDK streaming input changed");
			const streamInput = new Function("ce", "Se", "Yn", `return (${streamSource.replace("async streamInput", "async function")});`)(() => {}, JSON.stringify, Error);
			const writtenInput = [];
			await streamInput.call({ transport: { write: value => writtenInput.push(JSON.parse(value)), endInput() {} }, hasBidirectionalNeeds: () => false }, locallyQueuedGuidance);
			assert.equal(writtenInput.length, 0, "guidance buffered locally crossed the Stop fence");
			await streamInput.call({ transport: { write: value => writtenInput.push(JSON.parse(value)), endInput() {} }, hasBidirectionalNeeds: () => false }, [{ type: "user", uuid: "after-stop", message: { role: "user", content: "new authorized prompt" } }]);
			assert.equal(writtenInput[0].uuid, "after-stop", "Stop must not suppress a new later prompt");
			await assert.rejects(prototype.steer.call(stopBridge, { sessionId: "test", prompt: [{ type: "text", text: "racing guidance" }], _meta: { steering: { priority: "next" } } }), /atomic Stop support/);
			stopSession.cancelled = false;
			stopSession.aoInterruptCancelQueued = false;
			await assert.rejects(prototype.steer.call(stopBridge, { sessionId: "test", prompt: [{ type: "text", text: "unsupported native CLI" }], _meta: { steering: { priority: "next" } } }), /atomic Stop support/);
			stopSession.aoInterruptCancelQueued = true;
			for (const badReceipt of [{}, { still_queued: ["survivor"], cancelled: [] }, { still_queued: [123], cancelled: [] }, { still_queued: [null], cancelled: [] }, { still_queued: [{}], cancelled: [] }, { still_queued: [], cancelled: [123] }]) {
				stopSession.queryClosed = false;
				stopSession.cancelled = false;
				stopSession.query.request = async () => ({ response: badReceipt });
				let closed = false;
				stopSession.query.close = () => { closed = true; };
				stopSession.settingsManager = { dispose() {} };
				stopSession.input.end = () => {};
				await assert.rejects(prototype.cancel.call(stopBridge, { sessionId: "test" }), /did not confirm|malformed native Stop receipt/);
				assert.equal(stopSession.queryClosed, true, "unproven Stop left the native query open");
				assert.equal(closed, true, "unproven Stop did not close the query process");
				await assert.rejects(prototype.steer.call(stopBridge, { sessionId: "test", prompt: [{ type: "text", text: "after failed Stop" }], _meta: { steering: { priority: "next" } } }), /session.*ended/i);
			}
			const teardownAbort = new AbortController();
			stopSession.queryClosed = false;
			stopSession.abortController = teardownAbort;
			stopSession.query.request = async () => ({ response: { still_queued: [null], cancelled: [] } });
			stopBridge.logger = { error() {} };
			await prototype.teardownSession.call(stopBridge, "test");
			assert.equal(teardownAbort.signal.aborted, true, "failed Stop bypassed teardown abort");
			assert.equal(stopBridge.sessions.test, undefined, "failed Stop bypassed session removal");
			}
			console.log("PASS: pinned bridge keeps human guidance noninterrupting and preserves pending human questions; worker-only deferral and explicit Stop retain semantics");
		} finally {
			rmSync(copy, { force: true });
		}
	}
	console.log("PASS: worker envelope/next, typed automatic deferral, real-stop precedence, human steering and metadata validation");
} finally {
	rmSync(directory, { recursive: true, force: true });
}
