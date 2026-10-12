// Opt-in model test. Run with the pinned acp-agent.js and native Claude paths.
import assert from "node:assert/strict";
import { cpSync, mkdirSync, mkdtempSync, rmSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { patchClaudeQueuedStop, patchClaudeWorkerSteering } from "./build-acp-runtime-helpers.mjs";

assert.ok(process.argv[2] && process.argv[3], "Pass the pinned acp-agent.js and native Claude executable paths.");
const directory = mkdtempSync(join(tmpdir(), "ao-native-worker-check-"));
const config = join(directory, "config");
const cwd = join(directory, "workspace");
mkdirSync(config);
mkdirSync(cwd);
process.env.CLAUDE_CONFIG_DIR = config;
process.env.CLAUDE_CODE_EXECUTABLE = resolve(process.argv[3]);
process.env.CLAUDE_CODE_DISABLE_AUTO_MEMORY = "1";
process.env.CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = "1";
delete process.env.CLAUDECODE;
delete process.env.CLAUDE_CODE_SESSION_ID;
const nativeAdapter = resolve(process.argv[2]);
const runtime = join(directory, "runtime");
cpSync(dirname(dirname(nativeAdapter)), runtime, { recursive: true });
symlinkSync(dirname(dirname(dirname(dirname(nativeAdapter)))), join(directory, "node_modules"));
const adapter = join(runtime, "dist", "acp-agent.js");
patchClaudeWorkerSteering(adapter);
const sdkPackage = join(runtime, "node_modules", "@anthropic-ai", "claude-agent-sdk");
cpSync(join(dirname(dirname(dirname(dirname(nativeAdapter)))), "@anthropic-ai", "claude-agent-sdk"), sdkPackage, { recursive: true });
patchClaudeQueuedStop(join(sdkPackage, "sdk.mjs"));
const raw = [];
const forms = new Map();
const workerToken = "WORKER_54_NATIVE_PROOF_7c1e";
const answerToken = "HUMAN_ANSWER_BLUE_PROOF_2f9a";
let text = "";
let formReady;
const firstForm = new Promise(resolveForm => { formReady = resolveForm; });
let agent;
let timer;
const deadline = new Promise((_, reject) => {
	timer = setTimeout(() => reject(new Error("Native worker test exceeded 150 seconds.")), 150_000);
});
const bounded = promise => Promise.race([promise, deadline]);
try {
	const { ClaudeAcpAgent } = await import(pathToFileURL(adapter).href);
	agent = new ClaudeAcpAgent({
		sessionUpdate: async ({ update }) => {
			if (update.sessionUpdate === "agent_message_chunk" && update.content?.type === "text") text += update.content.text;
		},
		extNotification: async (method, { message }) => {
			if (method === "_claude/sdkMessage") {
				raw.push(message);
				if (message.type === "command_lifecycle") console.log(`native command ${message.state}`);
			}
		},
		requestPermission: async () => { throw new Error("Unexpected permission request; no tools except AskUserQuestion are allowed."); },
		unstable_createElicitation: (request, signal) => new Promise((respond, reject) => {
			assert.equal(request._meta?.aoWorkerQuestionYield, true);
			const form = { request, signal, respond, status: "pending", providerPending: true, aborted: false };
			forms.set(request.toolCallId, form);
			signal.addEventListener("abort", () => { form.aborted = true; reject(new Error("SDK question signal aborted")); }, { once: true });
			formReady(form);
			console.log("native AskUserQuestion is open and unanswered");
		}),
	}, { log() {}, error(message) { console.error(String(message)); } });
	const initialized = await agent.initialize({ protocolVersion: 1, clientCapabilities: { elicitation: { form: {} } } });
	assert.equal(initialized._meta.steering.aoWorkerQuestionYield, true);
	const session = await bounded(agent.newSession({
		cwd, mcpServers: [],
		_meta: {
			systemPrompt: "You are a test assistant. Follow the synthetic task using the supplied question tool. Worker updates can be reported independently of the question's answer.",
			claudeCode: {
				emitRawSDKMessages: [{ type: "user" }, { type: "assistant" }, { type: "result" }, { type: "command_lifecycle" }],
				options: {
					model: "haiku", tools: ["AskUserQuestion"], settingSources: [], strictMcpConfig: true,
					persistSession: false, maxBudgetUsd: 0.50, maxTurns: 6, thinking: { type: "disabled" },
					settings: { disableAllHooks: true, permissions: { defaultMode: "default" } },
				},
			},
		},
	}));
	const prompt = agent.prompt({ sessionId: session.sessionId, prompt: [{ type: "text", text: "Call AskUserQuestion exactly once. Ask 'Which color?' with options Blue and Red. After that, report any independent worker token you receive. Do not invent a color selection." }] });
	prompt.catch(() => {});
	const form = await bounded(firstForm);
	const injected = await bounded(agent.steer({
		sessionId: session.sessionId,
		prompt: [{ type: "text", text: `Independent worker 54 finished. Its result token is ${workerToken}. Report this exact token. This worker update contains no answer to the color question.` }],
		_meta: { steering: { priority: "next", senderSessionId: "worker-54", idleBehavior: "promptRequired" } },
	}));
	assert.equal(injected.outcome, "injected");
	assert.equal(injected.workerPending, true);
	// Mirror AO's typed deferral. Keep the durable form pending and answerable.
	for (const pending of forms.values()) {
		pending.providerPending = false;
		pending.respond({ action: "cancel", _meta: { aoWorkerQuestionYield: "automatic-worker-deferral" } });
	}
	const result = await bounded(prompt);
	assert.equal(result.stopReason, "end_turn");
	assert.ok(text.includes(workerToken), `Worker was not processed. Assistant text: ${text}`);
	assert.equal(form.status, "pending");
	assert.equal(form.aborted, false);
	assert.equal(form.signal.aborted, false);
	const denial = raw.flatMap(message => message.message?.content ?? []).find(block => block.type === "tool_result" && String(block.content).includes("automatically deferred"));
	assert.ok(denial, "Native tool result did not preserve automatic-deferral provenance.");
	assert.ok(!JSON.stringify(raw).includes("The user doesn't want to take this action right now. STOP"), "Native CLI generated false user-stop wording.");
	assert.ok(!/user (?:asked|requested|told).*stop/i.test(text), "Assistant attributed a worker update to a user stop.");
	console.log("PASS: native worker processed; original form remains pending; SDK signal was not cancelled; no synthetic user STOP");
	const beforeAnswer = text.length;
	const answer = await bounded(agent.prompt({ sessionId: session.sessionId, prompt: [{ type: "text", text: `Response to the earlier unanswered question 'Which color?': Blue. Report exactly ${answerToken} to acknowledge this answer.` }] }));
	assert.equal(answer.stopReason, "end_turn");
	assert.ok(text.slice(beforeAnswer).includes(answerToken), "Delayed human answer was not processed.");
	form.status = "resolved";
	console.log("PASS: native continuation processes the delayed human answer");
	const stoppedForm = new Promise(resolveForm => { formReady = resolveForm; });
	const stopTurn = agent.prompt({ sessionId: session.sessionId, prompt: [{ type: "text", text: "Call AskUserQuestion again. Ask 'Which shape?' with options Circle and Square. Do not choose for me." }] });
	stopTurn.catch(() => {});
	await bounded(stoppedForm);
	await bounded(agent.cancel({ sessionId: session.sessionId }));
	const stopped = await bounded(stopTurn);
	assert.equal(stopped.stopReason, "cancelled");
	console.log("PASS: an explicit native stop still cancels the turn waiting on a question");
	const results = raw.filter(message => message.type === "result");
	console.log(JSON.stringify({ scope: "standalone pinned Claude ACP/native process; AO durable form and exactly-once behavior remain covered by Go tests", questions: forms.size, resultOrigins: results.map(message => message.origin ?? null), costUSD: results.reduce((sum, message) => sum + (message.total_cost_usd ?? 0), 0) }));
} finally {
	clearTimeout(timer);
	if (agent) await Promise.race([agent.dispose(), delay(5_000)]);
	rmSync(directory, { recursive: true, force: true });
}
