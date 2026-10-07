# Hide Automation Messages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persistent, reversible toolbar toggle that hides routine automation message cards without changing delivery, stored history, assistant responses, or actionable warnings.

**Architecture:** Extend the existing Zustand UI store with one renderer-local preference. Filter a copied presentation list inside `ChatWorkspace` after existing history rules and turn grouping; do not change snapshots, transport, or database rows. Add a toolbar button and a loaded-history hidden count with a Show action.

**Tech Stack:** Existing React, TypeScript, Zustand, native localStorage, i18next, Vitest/Testing Library, Electron Forge, and Go packaging scripts. Add no dependencies.

## Global Constraints

- Display only: hide cards identified by the structured `origin: "automation"` field.
- Keep delivery to the agent, database history, and assistant responses unchanged.
- Replace hidden cards with a small “12 automation messages hidden · Show” indicator.
- Save the preference across chats and app restarts.
- Keep approval requests, errors, and failed or uncertain message-delivery notices visible.
- No backend change, migration, scheduler, MCP change, credential change, or context-compaction change.
- Use existing files and dependencies. Do not refactor the large chat component or add a preference framework.
- Default to showing messages. Enable hiding explicitly after installation; Show disables hiding persistently.
- One preference applies to all chats in this desktop renderer profile, not one preference per task.
- Use native buttons, visible focus, `aria-pressed`, and at least 24×24 CSS-pixel targets.
- Do not infer origin or warning severity from text prefixes, regexes, report keywords, or sender names.
- Do not push, publish, install over the running app, stop workers, or alter updater settings during plan creation.
- Implementation and lab work belong in a locked worktree under `${OG_WORKTREE_ROOT:-$HOME/dev/worktrees}`.
- A production replacement, AO quit, or updater policy change requires Jason's approval at execution time.

---

## Verified baseline and scope

Verified on 2026-10-07 by reading files, fresh Git refs, the installed app plist, and the local AO API:

- Planning branch: `plan/hide-automation-messages`.
- Planning worktree: `~/dev/worktrees/ao_hide_automation_plan`.
- Source baseline: `b903d2ea32da98e5a8bd15facd0eda3340897639`, fetched `origin/main`.
- Installed app: `/Applications/Agent Orchestrator.app`, version `0.13.3`.
- Installed daemon build reports revision `99d4035e992c84da0c1aadf5c76c5f6710e9221a` and `vcs.modified=true`.
- A newer app update was visible in Jason's screenshot. Re-read installed/pending versions before deployment.
- Actual automation cards have `kind: "message"`, `role: "user"`, `origin: "automation"`. Some have no `delivery` field.
- `OriginMessage` renders those cards; its existing Hide report control only collapses the body.
- `useUiStore` already persists several simple preferences using localStorage.
- `Timeline` groups by turn and virtualizes histories above 20 groups.
- Approval/error activities are distinct from message cards. Some automation-looking steers are activities, not messages.
- `ConversationMessage` has no structured severity field. A free-form automation report may mention an error without an error activity.
- Locale parity tests require all English keys and the CLDR plural categories for every supported locale.
- Canonical release publication belongs to the private release conductor. A local `npm run package` is not publication.

**Warning ceiling:** Preserve every structured error/warning activity, failed-turn message, approval, and delivery warning. This frontend-only filter cannot recognize an error stated only in an unstructured routine report without inventing a text classifier. Such reports remain stored and available through Show. Do not claim semantic detection of every report's content. If Jason requires content-only alerts to be exempt, stop and request a separately scoped structured-severity change.

**Steer ceiling:** Leave activity-based steers visible. This request targets message cards with the exact message-origin field. Do not widen the predicate to hide activities.

**Deployment ceiling:** The installed app contains local modifications. This plan does not authorize replacing them with a fresh upstream build. Preserve or reconcile the installed customizations, or receive explicit approval for an upstream-base upgrade, before production installation.

Serena is pinned to the workflow repo and cannot read this AO tree. Native source reads supplied this plan. No implementation tests or builds have run during planning.

## File map

Modify only these production files for the feature:

- `frontend/src/renderer/stores/ui-store.ts`: persisted flag and failure-safe read/write.
- `frontend/src/renderer/components/chat/ChatWorkspace.tsx`: toolbar control, copied display projection, accurate counter.
- `frontend/src/renderer/components/chat/ChatTimelineItems.tsx`: preserve delivery warning text on visible origin cards.
- `frontend/src/renderer/i18n/en.json`, `zh-CN.json`, `ja.json`, `ko.json`, `es.json`, `fr.json`, `de.json`, `pt-BR.json`: labels and plural messages.

Extend existing tests:

- `frontend/src/renderer/stores/ui-store.test.ts`.
- `frontend/src/renderer/components/chat/ChatWorkspace.test.tsx`.

Run existing locale, composer, and provider-signal tests unchanged. Create no new production module, store, backend endpoint, or test framework.

## Execution setup

- [ ] Continue this held planning branch, or use a separate approved handoff. Do not edit another session's worktree.

```bash
cd "$HOME/dev/worktrees/ao_hide_automation_plan"
git status --short --branch
git worktree list --porcelain
```

Expected: the planning document is committed, the tree is locked, and no unrelated tracked edits exist. `.worktree-keep` is a local hold; never commit it.

- [ ] Read `AGENTS.md`, `docs/development.md`, and `frontend/docs/desktop-release.md` from this branch.
- [ ] Install clean frontend and shared UI dependencies. Do not symlink another checkout's `node_modules`.

```bash
npm --prefix packages/product-ui ci
npm --prefix frontend ci
```

- [ ] Establish the narrow baseline before changing code.

```bash
npm --prefix frontend test -- src/renderer/stores/ui-store.test.ts src/renderer/components/chat/ChatWorkspace.test.tsx src/renderer/i18n/instance.test.ts
npm --prefix frontend run typecheck
```

Expected: both commands exit 0. A pre-existing failure is a blocker to classify, not permission to weaken tests.

### Task 1: Persist one global visibility preference

**Files:**
- Modify: `frontend/src/renderer/stores/ui-store.ts`, `UiState`, preference keys, initializers, and store actions (baseline lines 108–258 and 304–369).
- Test: `frontend/src/renderer/stores/ui-store.test.ts`.
- Modify: the eight locale catalogs listed in the file map.

**Interfaces:**
- Consumes: existing `create<UiState>`, `getLocalStorage()`, `showGlobalToast(...)`, and `appI18n.t(...)`.
- Produces: `hideAutomationMessages: boolean` and `setHideAutomationMessages(hidden: boolean): void` on `useUiStore`.
- Storage key: `ao.chat.hideAutomationMessages`.
- Default: `false`. Only the exact stored string `"true"` enables hiding.

- [ ] **Step 1: Add failing store checks.** Append this suite using the existing `bootStore()` helper.

```typescript
const visibilityKey = "ao.chat.hideAutomationMessages";

describe("automation message visibility", () => {
	beforeEach(() => {
		window.localStorage.clear();
		vi.resetModules();
		useUiStore.setState({ hideAutomationMessages: false });
		useUiStore.getState().clearGlobalToast();
	});

	it("shows messages by default and ignores invalid stored values", async () => {
		expect((await bootStore()).getState().hideAutomationMessages).toBe(false);
		localStorage.setItem(visibilityKey, "not-a-boolean");
		vi.resetModules();
		expect((await bootStore()).getState().hideAutomationMessages).toBe(false);
	});

	it("persists hiding and revealing across a fresh module boot", async () => {
		useUiStore.getState().setHideAutomationMessages(true);
		expect(localStorage.getItem(visibilityKey)).toBe("true");
		expect((await bootStore()).getState().hideAutomationMessages).toBe(true);
		useUiStore.getState().setHideAutomationMessages(false);
		expect(localStorage.getItem(visibilityKey)).toBe("false");
		vi.resetModules();
		expect((await bootStore()).getState().hideAutomationMessages).toBe(false);
	});

	it("fails open to showing messages when storage cannot be read", async () => {
		const read = vi.spyOn(Storage.prototype, "getItem").mockImplementation((key) => {
			if (key === visibilityKey) throw new DOMException("blocked", "SecurityError");
			return null;
		});
		try {
			expect((await bootStore()).getState().hideAutomationMessages).toBe(false);
		} finally {
			read.mockRestore();
		}
	});

	it("keeps the current choice usable and reports a failed write", () => {
		const write = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
			throw new DOMException("full", "QuotaExceededError");
		});
		try {
			expect(() => useUiStore.getState().setHideAutomationMessages(true)).not.toThrow();
			expect(useUiStore.getState().hideAutomationMessages).toBe(true);
			expect(useUiStore.getState().globalToast?.title).toBe("Display preference was not saved");
		} finally {
			write.mockRestore();
		}
	});
});
```

- [ ] **Step 2: Run the new suite and observe RED.**

```bash
npm --prefix frontend test -- src/renderer/stores/ui-store.test.ts -t "automation message visibility"
```

Expected: missing `setHideAutomationMessages` or wrong default assertions fail. Save the actual output, not a predicted test count.

- [ ] **Step 3: Add the minimal store state and action.** Add the type fields, initializer, and action to the existing store; do not change unrelated initializers.

```typescript
import { appI18n } from "../i18n";

// Add inside UiState:
hideAutomationMessages: boolean;
setHideAutomationMessages: (hidden: boolean) => void;

// Add beside the existing preference keys and initializers:
const hideAutomationMessagesStorageKey = "ao.chat.hideAutomationMessages";
function initialHideAutomationMessages(): boolean {
	try {
		return getLocalStorage()?.getItem(hideAutomationMessagesStorageKey) === "true";
	} catch {
		return false;
	}
}

// Add inside the existing create<UiState> object:
hideAutomationMessages: initialHideAutomationMessages(),
setHideAutomationMessages: (hideAutomationMessages) => {
	set({ hideAutomationMessages });
	try {
		const storage = getLocalStorage();
		if (!storage) throw new Error("Storage unavailable");
		storage.setItem(hideAutomationMessagesStorageKey, String(hideAutomationMessages));
	} catch {
		get().showGlobalToast(
			appI18n.t("chat.automation.storageTitle"),
			appI18n.t("chat.automation.storageBody"),
			{ tone: "error", dedupeKey: "automation-visibility-storage" },
		);
	}
},
```

Add this deliberate ceiling comment beside the new key:

```typescript
// ponytail: Renderer-local preference; add storage-event sync if separate AO windows need live synchronization.
```

- [ ] **Step 4: Add copy to all catalogs.** Insert these entries in the respective existing JSON objects. Keep every other entry unchanged.

```json
{
  "en": {
    "chat.automation.hide": "Hide automation messages",
    "chat.automation.show": "Show",
    "chat.automation.hidden_one": "{{count}} automation message hidden",
    "chat.automation.hidden_other": "{{count}} automation messages hidden",
    "chat.automation.scope": "Count covers loaded conversation history.",
    "chat.automation.storageTitle": "Display preference was not saved",
    "chat.automation.storageBody": "This choice works for this window but may reset after restart."
  },
  "zh-CN": {
    "chat.automation.hide": "隐藏自动化消息",
    "chat.automation.show": "显示",
    "chat.automation.hidden_one": "已隐藏 {{count}} 条自动化消息",
    "chat.automation.hidden_other": "已隐藏 {{count}} 条自动化消息",
    "chat.automation.scope": "数量仅包含已加载的对话历史。",
    "chat.automation.storageTitle": "显示偏好未保存",
    "chat.automation.storageBody": "此选择在当前窗口中有效，但重启后可能重置。"
  },
  "ja": {
    "chat.automation.hide": "自動化メッセージを非表示",
    "chat.automation.show": "表示",
    "chat.automation.hidden_one": "自動化メッセージ {{count}} 件を非表示",
    "chat.automation.hidden_other": "自動化メッセージ {{count}} 件を非表示",
    "chat.automation.scope": "件数は読み込み済みの会話履歴のみを対象とします。",
    "chat.automation.storageTitle": "表示設定を保存できませんでした",
    "chat.automation.storageBody": "現在のウィンドウでは有効ですが、再起動後にリセットされる可能性があります。"
  },
  "ko": {
    "chat.automation.hide": "자동화 메시지 숨기기",
    "chat.automation.show": "표시",
    "chat.automation.hidden_one": "자동화 메시지 {{count}}개 숨김",
    "chat.automation.hidden_other": "자동화 메시지 {{count}}개 숨김",
    "chat.automation.scope": "개수는 불러온 대화 기록에만 적용됩니다.",
    "chat.automation.storageTitle": "표시 설정이 저장되지 않았습니다",
    "chat.automation.storageBody": "현재 창에서는 적용되지만 재시작 후 초기화될 수 있습니다."
  },
  "es": {
    "chat.automation.hide": "Ocultar mensajes de automatización",
    "chat.automation.show": "Mostrar",
    "chat.automation.hidden_one": "{{count}} mensaje de automatización oculto",
    "chat.automation.hidden_many": "{{count}} mensajes de automatización ocultos",
    "chat.automation.hidden_other": "{{count}} mensajes de automatización ocultos",
    "chat.automation.scope": "El recuento incluye solo el historial de conversación cargado.",
    "chat.automation.storageTitle": "No se guardó la preferencia de visualización",
    "chat.automation.storageBody": "La elección funciona en esta ventana, pero puede restablecerse al reiniciar."
  },
  "fr": {
    "chat.automation.hide": "Masquer les messages d’automatisation",
    "chat.automation.show": "Afficher",
    "chat.automation.hidden_one": "{{count}} message d’automatisation masqué",
    "chat.automation.hidden_many": "{{count}} messages d’automatisation masqués",
    "chat.automation.hidden_other": "{{count}} messages d’automatisation masqués",
    "chat.automation.scope": "Le nombre concerne uniquement l’historique de conversation chargé.",
    "chat.automation.storageTitle": "La préférence d’affichage n’a pas été enregistrée",
    "chat.automation.storageBody": "Ce choix fonctionne dans cette fenêtre, mais peut être réinitialisé au redémarrage."
  },
  "de": {
    "chat.automation.hide": "Automatisierungsnachrichten ausblenden",
    "chat.automation.show": "Anzeigen",
    "chat.automation.hidden_one": "{{count}} Automatisierungsnachricht ausgeblendet",
    "chat.automation.hidden_other": "{{count}} Automatisierungsnachrichten ausgeblendet",
    "chat.automation.scope": "Die Anzahl umfasst nur den geladenen Gesprächsverlauf.",
    "chat.automation.storageTitle": "Anzeigeeinstellung wurde nicht gespeichert",
    "chat.automation.storageBody": "Die Auswahl gilt für dieses Fenster, kann aber nach einem Neustart zurückgesetzt werden."
  },
  "pt-BR": {
    "chat.automation.hide": "Ocultar mensagens de automação",
    "chat.automation.show": "Mostrar",
    "chat.automation.hidden_one": "{{count}} mensagem de automação oculta",
    "chat.automation.hidden_many": "{{count}} mensagens de automação ocultas",
    "chat.automation.hidden_other": "{{count}} mensagens de automação ocultas",
    "chat.automation.scope": "A contagem inclui apenas o histórico de conversa carregado.",
    "chat.automation.storageTitle": "A preferência de exibição não foi salva",
    "chat.automation.storageBody": "A escolha funciona nesta janela, mas pode ser redefinida após reiniciar."
  }
}
```

- [ ] **Step 5: Verify and commit this deliverable.**

```bash
npm --prefix frontend test -- src/renderer/stores/ui-store.test.ts src/renderer/i18n/instance.test.ts
npm --prefix frontend run typecheck
git add frontend/src/renderer/stores/ui-store.ts frontend/src/renderer/stores/ui-store.test.ts frontend/src/renderer/i18n/en.json frontend/src/renderer/i18n/zh-CN.json frontend/src/renderer/i18n/ja.json frontend/src/renderer/i18n/ko.json frontend/src/renderer/i18n/es.json frontend/src/renderer/i18n/fr.json frontend/src/renderer/i18n/de.json frontend/src/renderer/i18n/pt-BR.json
git commit -m "feat: persist automation message visibility preference"
```

Expected: tests and typecheck exit 0; only the preference, its tests, and copy land.

### Task 2: Filter the display and preserve warnings

**Files:**
- Modify: `frontend/src/renderer/components/chat/ChatWorkspace.tsx`, ChatHeader actions (baseline 1962–1968), Timeline grouping (2798–2805), timeline wrapper (3246–3287).
- Modify: `frontend/src/renderer/components/chat/ChatTimelineItems.tsx`, `OriginMessage` (718–751), existing `DeliveryNote` (982–998).
- Test: `frontend/src/renderer/components/chat/ChatWorkspace.test.tsx`.

**Interfaces:**
- Consumes: Task 1's store fields, `ConversationItem`, `ConversationSnapshot.turns`, `TimelineGroup`, and existing grouping/virtualization.
- Produces: local `AutomationMessagesToggle(): ReactElement` and `canHideAutomationMessage(item: ConversationItem, protectedTurns: ReadonlySet<string>): boolean`.
- A routine card is a non-streaming user message with exact automation origin, no delivery field or accepted delivery.
- A queued, interrupted, cancelled, or failed parent turn always preserves its message card.
- All activities and assistant messages remain untouched. Unknown delivery states stay visible.

- [ ] **Step 1: Add an integration regression through the real chat surface.** Add this suite to the existing test file. It uses existing `humanMessage()`, `idleSnapshot()`, and the tooltip-aware `render()` helper.

```typescript
function visibilitySnapshot(): ConversationSnapshot {
	const base = idleSnapshot();
	const approval = chatFixture.items.find((item) => item.id === "a-7")!;
	const items: ConversationSnapshot["items"] = [
		{ ...humanMessage("Human says AUTOMATION but is still human"), id: "human", sequence: 1 },
		{ ...humanMessage("routine one"), id: "auto-1", origin: "automation", sequence: 2 },
		{ ...humanMessage("routine two"), id: "auto-2", origin: "automation", delivery: "accepted", sequence: 3 },
		{ ...humanMessage("Assistant response stays"), id: "assistant", role: "assistant", origin: "automation", sequence: 4 },
		{ ...humanMessage("uncertain report"), id: "uncertain", origin: "automation", delivery: "uncertain", sequence: 5 },
		{ ...humanMessage("failed delivery report"), id: "failed-delivery", origin: "automation", delivery: "failed", sequence: 6 },
		{ ...humanMessage("queued report"), id: "queued", origin: "automation", delivery: "queued", sequence: 7 },
		{ ...humanMessage("sending report"), id: "sending", origin: "automation", delivery: "sending", sequence: 8 },
		{ ...humanMessage("failed turn report"), id: "failed-turn-message", turnId: "failed-turn", origin: "automation", sequence: 9 },
		{ ...approval, id: "approval", turnId: "active-turn", sequence: 10 },
		{ kind: "activity", id: "error", sequence: 11, revision: 0, activityKind: "error", status: "failed", summary: "Transport failed", createdAt: "2026-10-07T12:00:00Z" },
		{ ...humanMessage("daemon notice stays"), id: "daemon", origin: "daemon", sequence: 12 },
		{ ...humanMessage("provider notice stays"), id: "provider", origin: "provider", sequence: 13 },
		{ ...humanMessage("streaming report stays"), id: "streaming", origin: "automation", streaming: true, sequence: 14 },
	];
	return {
		...base,
		controller: { state: "busy" },
		items,
		turns: [
			{ id: "active-turn", state: "running", requestedAt: "2026-10-07T12:00:00Z" },
			{ id: "failed-turn", state: "failed", requestedAt: "2026-10-07T11:59:00Z", completedAt: "2026-10-07T12:00:00Z", errorMessage: "Turn failed" },
		],
		latestSequence: 14,
	};
}

describe("automation visibility in chat", () => {
	beforeEach(() => {
		useUiStore.setState({ hideAutomationMessages: false });
		localStorage.removeItem("ao.chat.hideAutomationMessages");
	});
	afterEach(() => useUiStore.setState({ hideAutomationMessages: false }));

	it("hides routine cards only, reports the exact count, and restores original content", async () => {
		const user = userEvent.setup();
		const snapshot = visibilitySnapshot();
		const original = structuredClone(snapshot);
		const onSend = vi.fn();
		const view = render(<ChatWorkspace snapshot={snapshot} onSend={onSend} />);
		const toggle = screen.getByRole("button", { name: "Hide automation messages" });
		expect(toggle).toHaveAttribute("aria-pressed", "false");
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-pressed", "true");
		expect(screen.queryByText("routine one")).not.toBeInTheDocument();
		expect(screen.queryByText("routine two")).not.toBeInTheDocument();
		expect(screen.getByText("2 automation messages hidden")).toBeInTheDocument();
		for (const text of ["Human says AUTOMATION but is still human", "Assistant response stays", "uncertain report", "failed delivery report", "queued report", "sending report", "failed turn report", "Transport failed", "daemon notice stays", "provider notice stays", "streaming report stays"]) {
			expect(screen.getByText(text)).toBeInTheDocument();
		}
		expect(screen.getByText("Delivery uncertain — not retried automatically")).toBeInTheDocument();
		expect(screen.getByText("Not delivered")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Approve", exact: true })).toBeInTheDocument();
		expect(snapshot).toEqual(original);
		expect(onSend).not.toHaveBeenCalled();

		view.rerender(<ChatWorkspace snapshot={{ ...snapshot, conversationId: "other-conversation", sessionId: "other-session" }} onSend={onSend} />);
		expect(screen.getByRole("button", { name: "Hide automation messages" })).toHaveAttribute("aria-pressed", "true");
		await user.click(screen.getByRole("button", { name: "Show", exact: true }));
		expect(screen.getByText("routine one")).toBeInTheDocument();
		expect(screen.getByText("routine two")).toBeInTheDocument();
		expect(localStorage.getItem("ao.chat.hideAutomationMessages")).toBe("false");
	});

	it("updates the count as loaded history grows and has no card-sized blank gaps", async () => {
		const user = userEvent.setup();
		const snapshot: ConversationSnapshot = {
			...idleSnapshot(), turns: [], latestSequence: 1,
			items: [{ ...humanMessage("hidden-only-card"), id: "one", origin: "automation" }],
		};
		const view = render(<ChatWorkspace snapshot={snapshot} hasOlder onLoadOlder={vi.fn()} />);
		await user.click(screen.getByRole("button", { name: "Hide automation messages" }));
		expect(screen.getByText("1 automation message hidden")).toBeInTheDocument();
		expect(view.container.querySelectorAll("[data-index]")).toHaveLength(0);
		expect(screen.getByRole("button", { name: "Load earlier messages" })).toBeInTheDocument();
		view.rerender(<ChatWorkspace snapshot={{ ...snapshot, latestSequence: 2, items: [...snapshot.items, { ...humanMessage("second-card"), id: "two", origin: "automation", sequence: 2 }] }} />);
		expect(screen.getByText("2 automation messages hidden")).toBeInTheDocument();
	});
});
```

- [ ] **Step 2: Observe RED before implementation.**

```bash
npm --prefix frontend test -- src/renderer/components/chat/ChatWorkspace.test.tsx -t "automation visibility in chat"
```

Expected: missing toolbar button fails. After initial wiring, warning, count, and restoration assertions exercise the real surface.

- [ ] **Step 3: Add the local toolbar control.** Use the existing Button and add `EyeOff` to the existing icon import.

```tsx
function AutomationMessagesToggle() {
	const { t } = useTranslation();
	const hidden = useUiStore((state) => state.hideAutomationMessages);
	const setHidden = useUiStore((state) => state.setHideAutomationMessages);
	return (
		<Button
			type="button"
			variant="ghost"
			size="icon-sm"
			aria-label={t("chat.automation.hide")}
			aria-pressed={hidden}
			title={t("chat.automation.hide")}
			className="min-h-6 min-w-6 shrink-0"
			onClick={() => setHidden(!hidden)}
		>
			<EyeOff aria-hidden="true" className="size-3.5" />
		</Button>
	);
}
```

In `ChatHeader`'s existing session-action region, retain its actions and insert the control:

```tsx
{tabStripAction ? <div data-testid="session-tab-strip-action">{tabStripAction}</div> : null}
{timelineActive ? <AutomationMessagesToggle /> : null}
{headerActions}
```

Keep the accessible name fixed; `aria-pressed` reports the setting. Do not disable a presentation preference while the agent is busy.

- [ ] **Step 4: Add the exact local visibility predicate.**

```typescript
function canHideAutomationMessage(item: ConversationItem, protectedTurns: ReadonlySet<string>): boolean {
	return item.kind === "message" && item.role === "user" && item.origin === "automation" &&
		!item.streaming && (item.delivery === undefined || item.delivery === "accepted") &&
		(!item.turnId || !protectedTurns.has(item.turnId));
}
```

Inside `Timeline`, subscribe to both store fields. Replace its existing `grouped` useMemo with this copied presentation projection. Preserve group keys and anchors by grouping before filtering.

```typescript
const hideAutomationMessages = useUiStore((state) => state.hideAutomationMessages);
const setHideAutomationMessages = useUiStore((state) => state.setHideAutomationMessages);
const { grouped, hiddenAutomationCount } = useMemo(() => {
	const hiddenTurns = hiddenTimelineTurnIds(snapshot);
	if (openingTurnId) hiddenTurns.delete(openingTurnId);
	const originalGroups = groupByTurn({ ...snapshot, items: timelineItems }).filter(
		(group) => !group.turnId || !hiddenTurns.has(group.turnId),
	);
	if (!hideAutomationMessages) return { grouped: originalGroups, hiddenAutomationCount: 0 };
	const protectedTurns = new Set(snapshot.turns.filter((entry) =>
		entry.state === "queued" || entry.state === "failed" || entry.state === "interrupted" || entry.state === "cancelled",
	).map((entry) => entry.id));
	let hiddenAutomationCount = 0;
	const grouped = originalGroups.flatMap((group) => {
		const visibleItems = group.items.filter((item) => !canHideAutomationMessage(item, protectedTurns));
		hiddenAutomationCount += group.items.length - visibleItems.length;
		if (visibleItems.length === group.items.length) return [group];
		if (!visibleItems.length && !group.live && !group.blocked && !group.plan && !group.diff &&
			group.outcome?.state !== "failed" && group.outcome?.state !== "interrupted") return [];
		return [{ ...group, items: visibleItems }];
	});
	return { grouped, hiddenAutomationCount };
}, [hideAutomationMessages, openingTurnId, snapshot, timelineItems]);
const groups = useStableList(grouped, groupKey, sameGroup);
```

Do not filter `snapshot.items` upstream. Pending requests, queue ownership, edits, rollback, delivery, and provider context keep reading the original snapshot.

- [ ] **Step 5: Add the indicator outside the scroll log.** Change only the Timeline wrapper to `relative flex min-h-0 flex-1 flex-col`. Change its existing scroll viewport from `h-full` to `min-h-0 flex-1`; retain all event handlers, refs, width, and scroll styles. Insert this above the scroller:

```tsx
{hiddenAutomationCount > 0 ? (
	<div className="shrink-0 px-4 py-1 text-xs text-muted-foreground">
		<div className="mx-auto flex max-w-3xl items-center gap-1" title={t("chat.automation.scope")}>
			<span>{t("chat.automation.hidden", { count: hiddenAutomationCount })}</span>
			<span aria-hidden="true">·</span>
			<button
				type="button"
				className="min-h-6 rounded px-1 text-markdown-link hover:underline focus-visible:outline-2 focus-visible:outline-ring"
				onClick={() => setHideAutomationMessages(false)}
			>
				{t("chat.automation.show")}
			</button>
		</div>
	</div>
) : null}
```

The count covers loaded, readable groups only. Unloaded rows and groups already suppressed by AO's human queue rules are not counted. Do not infer whether rolled-back rows are absent; count only the actual display projection. A new hidden card changes the count without reviving its card. Preserve the Load earlier messages button and running-turn status.

- [ ] **Step 6: Keep delivery warnings explicit on origin cards.** `OriginMessage` currently lacks the existing HumanMessage delivery-note block. Add this after its report body/expansion button and before its frame closes:

```tsx
{message.delivery && message.delivery !== "accepted" ? (
	<div className="mt-1.5">
		<DeliveryNote state={message.delivery} />
	</div>
) : null}
```

Do not add a text-only warning detector. Existing error, provider-failure, approval, system-warning, and steer activities bypass the filter.

- [ ] **Step 7: Extend the same regression suite for keyboard and long histories.** Put these tests inside `describe("automation visibility in chat", ...)`. Reuse that suite's setup and cleanup. This is a new code addition to the same test file, not a new framework.

```tsx
it("allows keyboard hiding and revealing without losing warnings", async () => {
	const user = userEvent.setup();
	render(<ChatWorkspace snapshot={visibilitySnapshot()} />);
	const toggle = screen.getByRole("button", { name: "Hide automation messages" });
	toggle.focus();
	await user.keyboard(" ");
	expect(toggle).toHaveAttribute("aria-pressed", "true");
	const show = screen.getByRole("button", { name: "Show", exact: true });
	show.focus();
	await user.keyboard("{Enter}");
	expect(screen.getByText("routine one")).toBeInTheDocument();
});

it("keeps errors and assistant replies in a virtualized history", async () => {
	stubVirtualTimelineGeometry();
	const user = userEvent.setup();
	const snapshot: ConversationSnapshot = {
		...idleSnapshot(),
		turns: Array.from({ length: 25 }, (_, index) => ({
			id: `history-${index}`, state: "completed" as const,
			requestedAt: "2026-10-07T12:00:00Z", completedAt: "2026-10-07T12:00:01Z",
		})),
		items: Array.from({ length: 25 }, (_, index) => [
			{ ...humanMessage(`report-${index}`), id: `report-${index}`, turnId: `history-${index}`, sequence: index * 3 + 1, origin: "automation" as const },
			{ ...humanMessage(`reply-${index}`), id: `reply-${index}`, turnId: `history-${index}`, sequence: index * 3 + 2, role: "assistant" as const, origin: "provider" as const },
			{ kind: "activity" as const, id: `warning-${index}`, turnId: `history-${index}`, sequence: index * 3 + 3, revision: 0, activityKind: "error" as const, status: "failed" as const, summary: `warning-${index}`, createdAt: "2026-10-07T12:00:00Z" },
		]).flat(), latestSequence: 75,
	};
	render(<ChatWorkspace snapshot={snapshot} />);
	await user.click(screen.getByRole("button", { name: "Hide automation messages" }));
	expect(screen.getByText("25 automation messages hidden")).toBeInTheDocument();
	expect(screen.queryByText("report-24")).not.toBeInTheDocument();
	expect(screen.getByText("reply-24")).toBeInTheDocument();
	expect(screen.getByText("warning-24")).toBeInTheDocument();
});
```

Append these tests inside the same suite. They verify turn-level safety when delivery metadata is missing and ensure queue groups excluded by existing rules do not inflate the count.

```tsx
it.each(["queued", "failed", "interrupted", "cancelled"] as const)(
	"keeps %s turn messages when delivery state is missing", async (state) => {
		const user = userEvent.setup();
		const snapshot: ConversationSnapshot = {
			...idleSnapshot(), latestSequence: 1,
			turns: [{ id: "protected", state, requestedAt: "2026-10-07T12:00:00Z" }],
			items: [{ ...humanMessage("protected automation card"), id: "protected-card", turnId: "protected", origin: "automation" }],
		};
		render(<ChatWorkspace snapshot={snapshot} />);
		await user.click(screen.getByRole("button", { name: "Hide automation messages" }));
		expect(screen.getByText("protected automation card")).toBeInTheDocument();
		expect(screen.queryByText(/automation message.*hidden/)).not.toBeInTheDocument();
	},
);

it("does not count cards inside a cancelled human queue group", async () => {
	const user = userEvent.setup();
	const snapshot: ConversationSnapshot = {
		...idleSnapshot(), latestSequence: 3,
		turns: [{ id: "queue-turn", state: "cancelled", requestedAt: "2026-10-07T12:00:00Z" }],
		items: [
			{ ...humanMessage("cancelled human prompt"), id: "old-human", turnId: "queue-turn" },
			{ ...humanMessage("already-suppressed card"), id: "old-card", turnId: "queue-turn", origin: "automation", sequence: 2 },
			{ ...humanMessage("current card"), id: "current-card", origin: "automation", sequence: 3 },
		],
	};
	render(<ChatWorkspace snapshot={snapshot} />);
	await user.click(screen.getByRole("button", { name: "Hide automation messages" }));
	expect(screen.getByText("1 automation message hidden")).toBeInTheDocument();
});
```

- [ ] **Step 8: Verify and commit the UI deliverable.**

```bash
npm --prefix frontend test -- src/renderer/components/chat/ChatWorkspace.test.tsx src/renderer/components/chat/ChatComposer.test.tsx src/renderer/components/chat/ChatProviderSignal.test.tsx src/renderer/stores/ui-store.test.ts src/renderer/i18n/instance.test.ts
npm --prefix frontend run typecheck
git diff --check
git add frontend/src/renderer/components/chat/ChatWorkspace.tsx frontend/src/renderer/components/chat/ChatTimelineItems.tsx frontend/src/renderer/components/chat/ChatWorkspace.test.tsx
git commit -m "feat: hide routine automation cards without hiding warnings"
```

Expected: all commands exit 0. Make no new test-only production seam. Do not weaken scroll, safety, or locale tests to make the toggle pass.

### Task 3: Validate, package, configure, and deploy safely

**Files:**
- No additional production changes.
- Update checkboxes in this plan as evidence is produced.
- Build outputs: `frontend/out/` (generated, not committed).
- Lab data: `~/.ao/labs/hide-automation/` (regenerable, not authored source).
- Pre-install backup: `~/.ao/backups/hide-automation-<UTC timestamp>/` (local, owner-only, not committed).

**Interfaces:**
- Consumes: Tasks 1–2's frontend commit, repository build scripts, Jason's separate installation approval.
- Produces: verified isolated preview, candidate bundle, and either a verified approved installation or a named deployment blocker.
- Configuration: click Hide automation messages once after installation. No shell-written Chromium state and no database write.

- [ ] **Step 1: Run the repository proof commands before delivery.**

```bash
npm --prefix frontend test
npm --prefix frontend run typecheck
npm --prefix frontend run typecheck:e2e
npm run lint
npm --prefix frontend run test:e2e:renderer
```

Expected: each exits 0. Read current `.github/workflows` first and run additional mandatory checks relevant to the diff. If a full suite cannot run locally, report that evidence as missing and require CI evidence before shipping. No release/publishing workflow is a validation command.

- [ ] **Step 2: Perform visual acceptance in an isolated development app.** Use a separate Electron profile as well as separate daemon data and run-file paths. Do not launch a packaged candidate concurrently against the production Electron profile.

```bash
cd "$HOME/dev/worktrees/ao_hide_automation_plan/frontend"
AO_DATA_DIR="$HOME/.ao/labs/hide-automation/data" AO_RUN_FILE="$HOME/.ao/labs/hide-automation/running.json" AO_DEV_ELECTRON_DIR="$HOME/.ao/labs/hide-automation/electron" AO_PORT=3015 npm run dev
```

First check port 3015 with `lsof -nP -iTCP:3015 -sTCP:LISTEN`. If occupied, stop and choose a new lab port; do not kill its owner. Run the UI acceptance against synthetic fixture data or throwaway lab sessions, not a cloned production database with live task ownership. Use the documented `dev:web` fixture if starting a lab agent would require credentials. Record that it is fixture evidence, not live-delivery proof.

Acceptance checklist:

- Toggle is reachable by keyboard and reports pressed/unpressed state.
- Routine cards disappear; assistant replies remain readable.
- Exact loaded-history count and Show action remain visible.
- Switch chats; the preference applies consistently.
- Stop/relaunch the lab app; the preference survives.
- Show restores cards in the original order and clears the saved hide flag.
- Structured approval, error, provider-warning, failed-turn, queued, sending, failed-delivery, and uncertain-delivery cases stay visible.
- Incoming cards update the count; long histories retain scrolling, minimap, and virtualization.
- Check both themes, 200% zoom, and narrow widths.
- Confirm toggle/Show make no delivery request. Their changes are renderer-local only.

Capture a before/after screenshot with the user's requested browser/screenshot tooling during execution. Embed the screenshots in the execution report. Do not invoke browser automation unless Jason has authorized its use.

- [ ] **Step 3: Produce a macOS arm64 candidate, not a release.**

```bash
cd "$HOME/dev/worktrees/ao_hide_automation_plan/frontend"
npm run package -- --platform=darwin --arch=arm64
```

Expected: the candidate is `frontend/out/agent-orchestrator-darwin-arm64/Agent Orchestrator.app` relative to the worktree. Check the actual Forge output before using this path. This command builds a bundled daemon/runtime as part of packaging even though the feature diff is frontend-only. An unsigned output is a lab artifact, not an approved production installation.

- [ ] **Step 4: Resolve the installed-base and signing gates before production.**

```bash
go version -m '/Applications/Agent Orchestrator.app/Contents/Resources/daemon/ao'
/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' '/Applications/Agent Orchestrator.app/Contents/Info.plist'
```

The measured installed daemon was locally modified. Do not overwrite its changes using an upstream bundle merely because these frontend tests passed. Jason must approve upgrading that customized installation or identify an exact reconciled source baseline. If the baseline differs, replay only the reviewed feature commits on an owned branch, rerun Task 3, and state the resulting full source SHA.

A production app must pass AO's existing signature/Gatekeeper/notarization verifier:

```bash
cd "$HOME/dev/worktrees/ao_hide_automation_plan"
bash frontend/scripts/verify-mac-artifact.sh 'frontend/out/agent-orchestrator-darwin-arm64/Agent Orchestrator.app'
```

If signing/notarization is unavailable, stop production deployment. Use the unsigned build only in the isolated lab. Do not remove quarantine, bypass Gatekeeper, ad-hoc sign the installed app, or patch its `app.asar`. Canonical signed distribution must go through `frontend/docs/desktop-release.md` and the designated private conductor. No `npm run publish`, release tag, substitute updater feed, or borrowed credentials.

- [ ] **Step 5: Obtain installation and stop-work approval.** Show Jason the candidate source SHA, app version, verifier result, installed customization reconciliation, and active task list. Ask permission to checkpoint and quit AO before replacement. Do not terminate workers to simplify installation. Pending ShipIt/updater activity is a blocker; the screenshot showed a staged update, so re-read it before proceeding.

For a persistent local customization, explicitly record the tradeoff: an upstream update can remove the toggle until it is upstreamed. Disabling automatic updates is a separate approval, not part of this feature. Do not mix an updater policy change into the frontend commits.

- [ ] **Step 6: Create recoverable owner-only backups after AO is fully stopped.** These are operator-executed deployment commands, not authorization to stop tasks during planning.

```bash
umask 077
BACKUP="$HOME/.ao/backups/hide-automation-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$BACKUP"
/usr/bin/ditto '/Applications/Agent Orchestrator.app' "$BACKUP/Agent Orchestrator.app"
sqlite3 "$HOME/.ao/data/ao.db" ".backup '$BACKUP/ao.db'"
/usr/bin/ditto "$HOME/.ao/claude-9router" "$BACKUP/claude-9router"
/usr/bin/ditto "$HOME/.ao/electron" "$BACKUP/electron"
printf '%s\n' "$BACKUP"
```

Before the copies, verify AO and its ShipIt are no longer running. Read the repository repair-install runbook's guard conditions. Backups contain credentials/history; do not print, upload, commit, or place them in a public artifact. Keep the exact backup path for rollback. The rest of `~/.ao/data` remains in place and must not be deleted or relocated.

- [ ] **Step 7: Replace the approved stopped app without deleting the original.** After verified backups and Jason's current-turn replacement approval, use a unique staged app directory. Do not run if the candidate or backup identity differs from Step 4.

```bash
CANDIDATE="$HOME/dev/worktrees/ao_hide_automation_plan/frontend/out/agent-orchestrator-darwin-arm64/Agent Orchestrator.app"
STAGED="/Applications/Agent Orchestrator-hide-automation-$(date -u +%Y%m%dT%H%M%SZ).app"
/usr/bin/ditto "$CANDIDATE" "$STAGED"
bash "$HOME/dev/worktrees/ao_hide_automation_plan/frontend/scripts/verify-mac-artifact.sh" "$STAGED"
```

Stop if either command fails. The staged bundle is not installation. Only after the verifier succeeds and the app/updater are still stopped:

```bash
mv '/Applications/Agent Orchestrator.app' "$BACKUP/Agent Orchestrator-replaced.app"
if mv "$STAGED" '/Applications/Agent Orchestrator.app'; then
  open '/Applications/Agent Orchestrator.app'
else
  mv "$BACKUP/Agent Orchestrator-replaced.app" '/Applications/Agent Orchestrator.app'
  exit 1
fi
```

These app moves require Jason's explicit approval. Do not invent an authorization receipt. On denied permissions, ask Jason to run the approved commands; do not thaw or bypass a guard.

- [ ] **Step 8: Configure and verify the actual installed surface.** Click Hide automation messages. Verify loaded routine automation cards disappear, the count appears, and Show restores them. Switch to a different existing chat and verify the same preference. At an approved idle point, quit/relaunch and verify persistence.

Verify the underlying snapshot still contains automation items. This read-only command reports metadata counts, not private message text:

```bash
curl --silent --show-error --max-time 15 http://127.0.0.1:3001/api/v1/sessions/og-workflow-31/conversation | python3 -c 'import json,sys; d=json.load(sys.stdin); rows=d.get("messages",[]); print("automation messages retained:",sum(m.get("origin")=="automation" for m in rows)); print("conversation:",d.get("conversationId"))'
```

Re-read the current orchestrator ID before using this command; the measured ID may change. Renderer count and API count can differ because the renderer suppresses some human queue groups and only counts hideable loaded messages. Compare the corresponding population, not raw totals. Confirm ongoing workers and reports still function; do not send a new agent instruction merely to verify the UI toggle.

- [ ] **Step 9: Roll back only if deployment acceptance fails.** Quit AO after checkpoint approval. Preserve the failed candidate and replace it with the verified app backup. Do not restore an old database over new work automatically. A bundled-daemon upgrade may have migrated the database; if the older app refuses the current schema, request a coordinated data restore and account for every post-install task/message first.

```bash
mv '/Applications/Agent Orchestrator.app' "$BACKUP/Agent Orchestrator-failed-candidate.app"
/usr/bin/ditto "$BACKUP/Agent Orchestrator.app" '/Applications/Agent Orchestrator.app'
open '/Applications/Agent Orchestrator.app'
```

All moves/restores require Jason's current-turn approval. Repeated rollback attempts that would overwrite an existing backup filename are refused; use a new unique name.

## Self-review and completion gate

- [ ] Task 1 proves default, persistence, reload, and storage-failure handling.
- [ ] Task 2 proves exact-origin filtering, loaded-count accuracy, immutable snapshots, assistant retention, approvals, and delivery warnings.
- [ ] Task 3 proves the actual installed UI, preservation of data/delivery, and restart persistence—or explicitly reports deployment as blocked.
- [ ] The implementation diff changes no backend, transport, generated API, scheduler, credentials, or unrelated component behavior.
- [ ] No claim of detecting content-only warnings or activity-based automation steers.
- [ ] No claim that a successful package equals a signed release or completed installation.
- [ ] Read fresh source/installed version after any upstream update; revalidate changed anchors instead of applying stale line offsets.
- [ ] Report complete absolute changed-file paths, checks and failures, candidate identity, installation status, rollback path, and confidence.

**Skipped:** Per-task preferences, semantic report classifiers, cross-window synchronization, backend metadata changes, and release infrastructure changes. Add them only for a separately approved requirement.
