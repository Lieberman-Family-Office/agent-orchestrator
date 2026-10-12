import { expect, test } from "@playwright/test";
import { agentReadiness } from "../src/renderer/test/agent-readiness-fixtures";
import { installFakeAgent } from "./support/fake-bridge";

// Renderer acceptance only: mocked APIs prove display behavior, not real agent delivery.
test("automation visibility is reversible and persistent without suppressing warnings @T0", async ({ page }) => {
	const projectId = "automation-visibility";
	const sessions = ["visibility-one", "visibility-two"];
	const at = "2026-10-07T12:00:00Z";
	const writes: string[] = [];
	const sentTexts: string[] = [];
	let showApprovals = true;
	await page.emulateMedia({ reducedMotion: "reduce" });
	await installFakeAgent(page, {
		projectId,
		projectName: projectId,
		workers: sessions.map((id) => ({ id, provider: "codex", title: id, mode: "chat" })),
	});
	const message = (id: string, sequence: number, origin: string, text: string, role = "user", turnId?: string) => ({
		kind: "message", id, sequence, revision: 0, role, origin, text, turnId, streaming: false, createdAt: at,
	});
	const messages = [
		message("human", 1, "human", "Human prompt stays", "user", "active-turn"),
		...Array.from({ length: 12 }, (_, index) => message(`routine-${index}`, index + 2, "automation", `Routine automation report ${index}`)),
		message("reply", 14, "provider", "Assistant response stays", "assistant"),
		message("failure", 15, "automation", "Failed delivery turn stays", "user", "failed-turn"),
		message("daemon", 16, "daemon", "Daemon notice stays"),
		message("recovered", 19, "automation", "Recovered automation card stays", "user", "recovered-turn"),
		message("queued-routine", 20, "automation", "Queued automation report", "user", "queued-routine-turn"),
	];
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const pathname = new URL(route.request().url()).pathname;
		if (route.request().method() !== "GET") writes.push(pathname);
		if (pathname.endsWith("/conversation/messages") && route.request().method() === "POST") {
			sentTexts.push(route.request().postDataJSON().text);
			await route.fulfill({ status: 202, json: { turnId: "typed-queue", state: "queued", duplicate: false } });
		} else if (pathname.startsWith("/api/v1/agents/readiness")) {
			await route.fulfill({ json: { agents: [agentReadiness("codex", "Codex")] } });
		} else if (pathname === `/api/v1/projects/${projectId}`) {
			await route.fulfill({ json: { status: "ok", project: { id: projectId, agent: "codex", config: { worker: { agent: "codex" } } } } });
		} else if (sessions.some((id) => pathname === `/api/v1/sessions/${id}/conversation`)) {
			await route.fulfill({ json: {
				conversationId: `conversation-${pathname.split("/")[4]}`, sessionId: pathname.split("/")[4], harness: "codex", mode: "chat",
				controller: "busy", latestSequence: 23, oldestSequence: 1, hasMoreBefore: false, settings: {}, capabilities: ["approvals"],
				turns: [
					{ id: "active-turn", state: "running", requestedAt: at },
					{ id: "queued-routine-turn", state: "queued", requestedAt: at },
					{ id: "failed-turn", state: "failed", requestedAt: at, completedAt: at, errorMessage: "Delivery failed" },
					{ id: "recovered-turn", state: "recovered", requestedAt: at, completedAt: at },
				],
				messages,
				activities: (showApprovals ? [
					{ kind: "activity", id: "approval", turnId: "active-turn", sequence: 17, revision: 0, activityKind: "approval", status: "pending", summary: "Explicit approval stays", requestId: "approval-one", detail: { decisions: [{ id: "accept", label: "Approve", kind: "allow_once" }, { id: "cancel", label: "Cancel", kind: "reject_once" }] }, createdAt: at },
					{ kind: "activity", id: "error", sequence: 18, revision: 0, activityKind: "error", status: "failed", summary: "Structured error stays", createdAt: at },
					{ kind: "activity", id: "worker-steer", turnId: "active-turn", sequence: 21, revision: 0, activityKind: "system", status: "completed", summary: "Routine worker steer", detail: { event: "steer", origin: "automation", text: "Routine worker steer", senderSessionId: "worker", senderProjectId: projectId, senderDisplayName: "Worker" }, createdAt: at },
					{ kind: "activity", id: "human-steer", turnId: "active-turn", sequence: 22, revision: 0, activityKind: "system", status: "completed", summary: "Human steer stays", detail: { event: "steer", origin: "human", text: "Human steer stays" }, createdAt: at },
					{ kind: "activity", id: "worker-warning", turnId: "active-turn", sequence: 23, revision: 0, activityKind: "system", status: "completed", summary: "Worker warning stays", detail: { event: "steer", origin: "automation", text: "Worker warning stays", senderSessionId: "worker", severity: "warning" }, createdAt: at },
				] : []),
			} });
		} else if (pathname.endsWith("/conversation/models")) {
			await route.fulfill({ json: { models: [], selected: {} } });
		} else if (pathname.endsWith("/conversation/skills")) {
			await route.fulfill({ json: { skills: [] } });
		} else if (pathname.endsWith("/workspace/files")) {
			await route.fulfill({ json: { files: [], truncated: false } });
		} else if (pathname.endsWith("/interface-transition")) {
			await route.fulfill({ json: { supported: true, targetMode: "tui" } });
		} else {
			await route.fulfill({ json: { status: "ok" } });
		}
	});

	await page.goto(`/#/projects/${projectId}/sessions/${sessions[0]}`);
	const toggle = page.getByRole("button", { name: "Hide automation messages" });
	await expect(toggle).toHaveAttribute("aria-pressed", "false");
	await expect(toggle.locator("svg")).toHaveClass(/lucide-eye(?:\s|$)/);
	await expect(page.getByText("Routine automation report 0", { exact: true })).toBeVisible();
	await expect(page.getByText("Queued automation report", { exact: true })).toBeVisible();
	const beforeToggleWrites = writes.length;
	await toggle.focus();
	await page.keyboard.press("Shift+Tab");
	await page.keyboard.press("Tab");
	await expect(toggle).toBeFocused();
	const toggleFocus = await toggle.evaluate((element) => {
		const style = getComputedStyle(element);
		return { width: parseFloat(style.outlineWidth), style: style.outlineStyle, color: style.outlineColor };
	});
	expect(toggleFocus.width).toBeGreaterThanOrEqual(2);
	expect(toggleFocus.style).toBe("solid");
	expect(toggleFocus.color).not.toBe("rgba(0, 0, 0, 0)");
	await page.keyboard.press("Space");
	await expect(toggle).toHaveAttribute("aria-pressed", "true");
	await expect(toggle.locator("svg")).toHaveClass(/lucide-eye-off(?:\s|$)/);
	await expect(page.getByText("14 automation messages hidden", { exact: true })).toBeVisible();
	await expect(page.getByText(/^Routine automation report /)).toHaveCount(0);
	await expect(page.getByText("Queued automation report", { exact: true })).toHaveCount(0);
	await expect(page.getByText("Routine worker steer", { exact: true })).toHaveCount(0);
	await expect(page.getByText("Human steer stays", { exact: true })).toBeVisible();
	await expect(page.getByText("Worker warning stays", { exact: true })).toBeVisible();
	for (const text of ["Human prompt stays", "Assistant response stays", "Failed delivery turn stays", "Daemon notice stays", "Structured error stays"]) {
		await expect(page.getByText(text, { exact: true })).toBeVisible();
	}
	await expect(page.getByRole("button", { name: /^Allow once/ })).toBeVisible();
	await expect(page.getByText("This turn was recovered from an earlier session", { exact: true })).toBeVisible();
	await expect(page.getByText("Recovered automation card stays", { exact: true })).toBeVisible();
	expect(writes.slice(beforeToggleWrites)).toEqual([]);
	expect(messages.filter((item) => item.origin === "automation")).toHaveLength(15);

	await page.goto(`/#/projects/${projectId}/sessions/${sessions[1]}`);
	await expect(toggle).toHaveAttribute("aria-pressed", "true");
	await expect(page.getByText("14 automation messages hidden", { exact: true })).toBeVisible();
	await page.reload();
	await expect(toggle).toHaveAttribute("aria-pressed", "true");
	await expect(page.getByText(/^Routine automation report /)).toHaveCount(0);
	const show = page.getByRole("button", { name: "Show", exact: true });
	await show.focus();
	await page.keyboard.press("Shift+Tab");
	await page.keyboard.press("Tab");
	await expect(show).toBeFocused();
	const showFocus = await show.evaluate((element) => {
		const style = getComputedStyle(element);
		return { width: parseFloat(style.outlineWidth), style: style.outlineStyle, color: style.outlineColor };
	});
	expect(showFocus.width).toBeGreaterThanOrEqual(2);
	expect(showFocus.style).toBe("solid");
	expect(showFocus.color).not.toBe("rgba(0, 0, 0, 0)");
	await page.keyboard.press("Enter");
	await expect(page.getByText("Routine automation report 0", { exact: true })).toBeVisible();
	await expect(page.getByText(/^Routine automation report /)).toHaveCount(12);
	await expect(toggle).toHaveAttribute("aria-pressed", "false");
	await expect(toggle.locator("svg")).toHaveClass(/lucide-eye(?:\s|$)/);
	await expect(page.getByText("Queued automation report", { exact: true })).toBeVisible();
	await expect(page.getByText("Routine worker steer", { exact: true })).toBeVisible();
	await expect(page.getByRole("log", { name: "Conversation" })).toBeFocused();
	expect(await page.evaluate(() => localStorage.getItem("ao.chat.hideAutomationMessages"))).toBe("false");
	showApprovals = false;
	await page.reload();
	const typed = "  Only my typed words\n\n  Preserve this spacing.\n\n";
	const composer = page.getByRole("combobox", { name: "Message the agent" });
	await composer.fill(typed);
	expect(await composer.textContent()).toBe(typed);
	await page.getByRole("button", { name: "Queue message", exact: true }).click();
	await expect.poll(() => sentTexts).toEqual([typed]);
});
