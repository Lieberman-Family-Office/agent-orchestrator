import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../../stores/ui-store";
import { typeInLexicalEditor, lexicalEditorText } from "../../test/lexical";
import { TooltipProvider } from "../ui/tooltip";
import { ChatComposer } from "./ChatComposer";
import { QueuedMessageDock } from "./QueuedMessageDock";

const compositionEvents = [
	{ name: "native isComposing", isComposing: true, keyCode: 13 },
	{ name: "keyCode 229 with isComposing false", isComposing: false, keyCode: 229 },
];

describe.each(["enter", "mod-enter"] as const)("IME Enter in %s mode", (mode) => {
	beforeEach(() => useUiStore.setState({ chatSendKeyMode: mode, chatSendMode: "steer" }));

	describe.each(compositionEvents)("$name", ({ isComposing, keyCode }) => {
		it.each([
			{ action: "send", delivery: "send", willQueue: false, metaKey: false, ctrlKey: false },
			{ action: "queue", delivery: "send", willQueue: true, metaKey: false, ctrlKey: false },
			{ action: "steer", delivery: "steer", willQueue: true, metaKey: false, ctrlKey: false },
			{ action: "Cmd+Enter steer", delivery: "steer", willQueue: true, metaKey: true, ctrlKey: false },
			{ action: "Ctrl+Enter steer", delivery: "steer", willQueue: true, metaKey: false, ctrlKey: true },
			{ action: "interrupt", delivery: "interrupt", willQueue: true, metaKey: false, ctrlKey: false },
		] as const)("does not $action, and retains the draft for the next Enter", async ({ delivery, willQueue, metaKey, ctrlKey }) => {
			useUiStore.setState({ chatSendMode: delivery === "interrupt" ? "interrupt" : "steer" });
			const onSend = vi.fn().mockResolvedValue(undefined);
			const onSteer = vi.fn().mockResolvedValue(undefined);
			const onInterruptSend = vi.fn().mockResolvedValue(undefined);
			render(
				<TooltipProvider>
					<ChatComposer onSend={onSend} onSteer={onSteer} onInterruptSend={onInterruptSend} canSteer={delivery !== "send"} willQueue={willQueue} />
				</TooltipProvider>,
			);
			const field = screen.getByLabelText("Message the agent");
			await typeInLexicalEditor(field, "日本語のメッセージ");

			const modifiers = { metaKey, ctrlKey: ctrlKey || (mode === "mod-enter" && !metaKey) };
			let defaultAllowed = false;
			await act(async () => {
				defaultAllowed = fireEvent.keyDown(field, { key: "Enter", isComposing, keyCode, ...modifiers });
			});

			expect(onSend).not.toHaveBeenCalled();
			expect(onSteer).not.toHaveBeenCalled();
			expect(onInterruptSend).not.toHaveBeenCalled();
			expect(defaultAllowed).toBe(true);
			expect(lexicalEditorText(field)).toBe("日本語のメッセージ");

			fireEvent.keyDown(field, { key: "Enter", ...modifiers });
			const deliveries = { send: onSend, steer: onSteer, interrupt: onInterruptSend };
			await waitFor(() => expect(deliveries[delivery]).toHaveBeenCalledExactlyOnceWith("日本語のメッセージ"));
			for (const [kind, callback] of Object.entries(deliveries)) {
				if (kind !== delivery) expect(callback).not.toHaveBeenCalled();
			}
		});

		it.each([
			{ action: "select a skill suggestion", draft: "/rev", menu: true },
			{ action: "select a file suggestion", draft: "@REA", menu: true },
			{ action: "select a command suggestion", draft: "/com", menu: true },
			{ action: "execute /compact", draft: "/compact ", menu: false },
		])("does not $action", async ({ menu, draft }) => {
			const onSend = vi.fn();
			const onSteer = vi.fn();
			const onCompact = vi.fn();
			render(
				<TooltipProvider>
					<ChatComposer
						onSend={onSend}
						onSteer={onSteer}
						onCompact={onCompact}
						canSteer
						willQueue
						skills={[{ name: "review", displayName: "review", description: "Review the diff", source: "user" }]}
						filePaths={["README.md"]}
					/>
				</TooltipProvider>,
			);
			const field = screen.getByLabelText("Message the agent");
			await typeInLexicalEditor(field, draft);
			if (menu) expect(screen.getByRole("listbox")).toBeInTheDocument();

			let defaultAllowed = false;
			await act(async () => {
				defaultAllowed = fireEvent.keyDown(field, { key: "Enter", isComposing, keyCode, ctrlKey: mode === "mod-enter" });
			});

			expect(onSend).not.toHaveBeenCalled();
			expect(onSteer).not.toHaveBeenCalled();
			expect(onCompact).not.toHaveBeenCalled();
			expect(defaultAllowed).toBe(true);
			expect(field.querySelector("[data-composer-token]")).toBeNull();
			expect(lexicalEditorText(field)).toBe(draft);
			if (menu) expect(screen.getByRole("listbox")).toBeInTheDocument();
		});

		it("does not steer the next queued message from an empty draft", async () => {
			const onSend = vi.fn();
			const onSteer = vi.fn();
			const onPromoteQueuedTurn = vi.fn().mockResolvedValue(undefined);
			render(
				<TooltipProvider>
					<ChatComposer
						onSend={onSend}
						onSteer={onSteer}
						canSteer
						willQueue
						queuedDock={
							<QueuedMessageDock
								messages={[{
									turnId: "queued-1",
									message: {
										kind: "message", id: "message-1", turnId: "queued-1", sequence: 1,
										revision: 0, role: "user", origin: "human", text: "next message",
										streaming: false, createdAt: "2026-08-11T10:01:00Z",
									},
								}]}
								canSteer
								onPromoteQueuedTurn={onPromoteQueuedTurn}
							/>
						}
					/>
				</TooltipProvider>,
			);
			const field = screen.getByLabelText("Message the agent");
			field.focus();

			await act(async () => {
				fireEvent.keyDown(field, { key: "Enter", isComposing, keyCode });
			});

			expect(onSend).not.toHaveBeenCalled();
			expect(onSteer).not.toHaveBeenCalled();
			expect(onPromoteQueuedTurn).not.toHaveBeenCalled();
			expect(screen.getByTestId("queued-message-queued-1")).toBeInTheDocument();

			if (mode === "enter") {
				fireEvent.keyDown(field, { key: "Enter" });
				await waitFor(() => expect(onPromoteQueuedTurn).toHaveBeenCalledWith("queued-1"));
			} else {
				await act(async () => {
					fireEvent.keyDown(field, { key: "Enter", ctrlKey: true });
				});
				expect(onPromoteQueuedTurn).not.toHaveBeenCalled();
			}
		});
	});
});
