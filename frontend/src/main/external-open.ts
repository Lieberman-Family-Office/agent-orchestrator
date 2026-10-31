import { realpath, stat } from "node:fs/promises";
import path from "node:path";

const APP_EXTERNAL_PROTOCOLS = new Set(["http:", "https:", "mailto:"]);
// macOS System Settings panes the renderer may open, matched exactly: the
// Privacy & Security panes where Cua Driver's permissions are managed.
const APP_EXTERNAL_EXACT_URLS = new Set([
	"x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
	"x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
]);

export type ExternalOpener = {
	openExternal: (url: string) => Promise<unknown>;
};

export function isAllowedAppExternalURL(rawUrl: string): boolean {
	if (APP_EXTERNAL_EXACT_URLS.has(rawUrl)) return true;
	try {
		const url = new URL(rawUrl);
		return APP_EXTERNAL_PROTOCOLS.has(url.protocol);
	} catch {
		return false;
	}
}

export async function openAllowedAppExternalURL(rawUrl: string, opener: ExternalOpener): Promise<void> {
	if (!isAllowedAppExternalURL(rawUrl)) {
		throw new Error("Unsupported external URL");
	}
	await opener.openExternal(rawUrl);
}

/** Main confines relative catalog paths and absolute chat references to the session root. */
export async function openSessionFileInDefaultApp(
	sessionId:string, reference:string, source:"workspace"|"artifact",
	resolveRoot:(sessionId:string,source:"workspace"|"artifact")=>Promise<string>,
	opener:{openPath:(file:string)=>Promise<string>},
):Promise<void> {
	if(typeof sessionId!=="string" || !sessionId || typeof reference!=="string" || !reference || reference.includes("\0") || (source!=="workspace" && source!=="artifact")) throw new Error("Invalid session file request");
	const root=await realpath(await resolveRoot(sessionId,source));
	const target=await realpath(path.resolve(root,reference));
	const relative=path.relative(root,target);
	if(!relative || relative===".." || relative.startsWith(".."+path.sep) || path.isAbsolute(relative) || !(await stat(target)).isFile()) throw new Error("File is outside the session root or is not a regular file");
	const error=await opener.openPath(target);
	if(error) throw new Error(error);
}
