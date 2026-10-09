import { expect, test } from "@playwright/test";
import { agentReadiness } from "../src/renderer/test/agent-readiness-fixtures";
import { installFakeAgent } from "./support/fake-bridge";

// Synthetic renderer/request proof; no production API or model is contacted.
test("one Enter follows the persistent Steer/Interrupt choice @T0", async ({ page }) => {
	const projectId = "send-mode";
	const sessionId = "send-mode-session";
	const at = "2026-10-09T01:00:00Z";
	const requests: Array<{ action: string; text: string; interruptAndSend?: boolean }> = [];
	await installFakeAgent(page, { projectId, projectName: projectId, workers: [{ id: sessionId, provider: "codex", title: sessionId, mode: "chat" }] });
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (route.request().method() === "POST" && (path.endsWith("/steer") || path.endsWith("/messages"))) {
			requests.push({ action: path.endsWith("/steer") ? "steer" : "send", ...route.request().postDataJSON() });
			await route.fulfill({ status: 202, json: path.endsWith("/steer") ? { accepted: true, kind: "steered", providerTurnId: "provider-active", activityId: "accepted-steer" } : { turnId: "interrupt-prompt", state: "queued", duplicate: false } });
		} else if (path.startsWith("/api/v1/agents/readiness")) {
			await route.fulfill({ json: { agents: [agentReadiness("codex", "Codex")] } });
		} else if (path === `/api/v1/projects/${projectId}`) {
			await route.fulfill({ json: { project: { id: projectId, agent: "codex", config: { worker: { agent: "codex" } } } } });
		} else if (path === `/api/v1/sessions/${sessionId}/conversation`) {
			await route.fulfill({ json: {
				conversationId: "send-mode-conversation", sessionId, harness: "codex", mode: "chat", controller: "busy", latestSequence: 1, oldestSequence: 1, hasMoreBefore: false,
				settings: {}, capabilities: ["steer", "interrupt"],
				turns: [{ id: "active", state: "running", requestedAt: at }],
				messages: [{ kind: "message", id: "opening", turnId: "active", sequence: 1, revision: 0, role: "user", origin: "human", text: "Opening prompt", streaming: false, createdAt: at }], activities: [],
			} });
		} else if (path.endsWith("/conversation/models")) await route.fulfill({ json: { models: [], selected: {} } });
		else if (path.endsWith("/conversation/skills")) await route.fulfill({ json: { skills: [] } });
		else if (path.endsWith("/workspace/files")) await route.fulfill({ json: { files: [], truncated: false } });
		else if (path.endsWith("/interface-transition")) await route.fulfill({ json: { supported: true, targetMode: "tui" } });
		else await route.fulfill({ json: { status: "ok" } });
	});
	await page.goto(`/#/projects/${projectId}/sessions/${sessionId}`);
	const mode = page.getByRole("combobox", { name: "Send while agent is working" });
	const field = page.getByRole("combobox", { name: "Message the agent" });
	await expect(mode).toHaveValue("steer");
	await field.fill("  steer with one Enter  ");
	await field.press("Enter");
	await expect.poll(() => requests.length).toBe(1);
	expect(requests[0]).toMatchObject({ action: "steer", text: "  steer with one Enter  " });
	await mode.selectOption("interrupt");
	await field.fill("interrupt with one Enter");
	await field.press("Enter");
	await expect.poll(() => requests.length).toBe(2);
	expect(requests[1]).toMatchObject({ action: "send", text: "interrupt with one Enter", interruptAndSend: true });
	await page.reload();
	await expect(mode).toHaveValue("interrupt");
});
