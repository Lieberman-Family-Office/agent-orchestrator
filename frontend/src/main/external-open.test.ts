import { afterEach, describe, expect, it, vi } from "vitest";
import { isAllowedAppExternalURL, openAllowedAppExternalURL, openSessionFileInDefaultApp } from "./external-open";
import { mkdtempSync, mkdirSync, writeFileSync, symlinkSync, realpathSync,rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

describe("session file default-app handoff", () => {
	const temporary:string[]=[];
	afterEach(()=>{for(const dir of temporary.splice(0)) rmSync(dir,{recursive:true,force:true});});
	it("opens only a regular file confined to the resolved session root", async () => {
		const root=mkdtempSync(path.join(tmpdir(),"ao-file-open-"));temporary.push(root);
		const file=path.join(root,"draft.txt");
		writeFileSync(file,"draft");
		const openPath=vi.fn().mockResolvedValue("");
		await openSessionFileInDefaultApp("session","draft.txt","artifact",async()=>root,{openPath});
		expect(openPath).toHaveBeenCalledWith(realpathSync(file));
		openPath.mockClear();
		await expect(openSessionFileInDefaultApp("session","../outside.txt","artifact",async()=>root,{openPath})).rejects.toThrow();
		const outside=mkdtempSync(path.join(tmpdir(),"ao-file-outside-"));temporary.push(outside);
		writeFileSync(path.join(outside,"secret.txt"),"secret");
		symlinkSync(outside,path.join(root,"escape"));
		await expect(openSessionFileInDefaultApp("session","escape/secret.txt","artifact",async()=>root,{openPath})).rejects.toThrow();
		mkdirSync(path.join(root,"directory"));
		await expect(openSessionFileInDefaultApp("session","directory","artifact",async()=>root,{openPath})).rejects.toThrow();
		expect(openPath).not.toHaveBeenCalled();
	});
});

describe("isAllowedAppExternalURL", () => {
	it("allows web and mail handoff URLs from the app renderer", () => {
		expect(isAllowedAppExternalURL("https://github.com/Untrivial-ai/agent-orchestrator/issues/new")).toBe(true);
		expect(isAllowedAppExternalURL("http://localhost:5173/help")).toBe(true);
		expect(isAllowedAppExternalURL("mailto:prateek@untrivial.ai?subject=AO%20feedback")).toBe(true);
	});

	it("blocks local, privileged, and script schemes", () => {
		expect(isAllowedAppExternalURL("file:///Users/alice/private.txt")).toBe(false);
		expect(isAllowedAppExternalURL("app://renderer/index.html")).toBe(false);
		expect(isAllowedAppExternalURL("javascript:alert(1)")).toBe(false);
	});

	it("opens allowed URLs through the native shell opener", async () => {
		const openExternal = vi.fn().mockResolvedValue(undefined);

		await openAllowedAppExternalURL("mailto:prateek@untrivial.ai?subject=AO%20feedback", { openExternal });

		expect(openExternal).toHaveBeenCalledWith("mailto:prateek@untrivial.ai?subject=AO%20feedback");
	});

	it("rejects unsupported URLs before reaching the shell opener", async () => {
		const openExternal = vi.fn().mockResolvedValue(undefined);

		await expect(openAllowedAppExternalURL("file:///Users/alice/private.txt", { openExternal })).rejects.toThrow(
			"Unsupported external URL",
		);
		expect(openExternal).not.toHaveBeenCalled();
	});
});
