import { setTimeout as pauseForRecording } from "node:timers/promises";
import { writeFile } from "node:fs/promises";
import { expect, test } from "/Users/dhruvsharma/.codex/worktrees/5522/agent-orchestrator-1/frontend/node_modules/@playwright/test/index.mjs";
import { installFakeAgent } from "/Users/dhruvsharma/.codex/worktrees/5522/agent-orchestrator-1/frontend/e2e/support/fake-bridge.ts";

test("Live display before and after while saved-history refresh waits", async ({ page }) => {
	const sessionId = "live-text";
    const mode = process.env.EVIDENCE_MODE ?? "AFTER";
    const chunks = ["The reply ", "appears ", "as each chunk arrives, ", "even while ", "saving is ", "paused."];
    const reply = chunks.join("");
    const observations: {phase:string; delivered:number; replyVisible:boolean; saved:boolean}[] = [];
    await page.emulateMedia({ reducedMotion: "no-preference" });
	await installFakeAgent(page, { workers: [{ id: sessionId, title: "Live text before saving", mode: "chat" }] });
	await page.addInitScript(() => {
		const add = EventSource.prototype.addEventListener;
		EventSource.prototype.addEventListener = function (type: string, callback: ((event: MessageEvent) => void) | EventListenerOrEventListenerObject, options?: boolean | AddEventListenerOptions) {
			if (type === "session_updated") {
                (window as unknown as { requestSavedText: () => void }).requestSavedText = () => { const event = new MessageEvent(type, {data: JSON.stringify({type:"session_updated",sessionId:"live-text",payload:{conversationId:"conversation"}})}); if (typeof callback === "function") callback.call(this,event); else callback.handleEvent(event); };
            }
            if (type === "conversation_text") {
				(window as unknown as { requestSavedText: () => void }).requestSavedText = () => this.onopen?.call(this, new Event("open"));
				(window as unknown as { sendLiveText: (frame: unknown) => void }).sendLiveText = (frame) => {
					const event = new MessageEvent(type, { data: JSON.stringify(frame) });
					if (typeof callback === "function") callback.call(this, event);
					else callback?.handleEvent(event);
				};
			}
			return add.call(this, type, callback as EventListenerOrEventListenerObject, options);
		};
	});
	let saved = false;
	let hold = false;
	let release!: () => void;
	let requestBlocked!: () => void;
	const blocked = new Promise<void>((resolve) => { requestBlocked = resolve; });
	const waiting = new Promise<void>((resolve) => { release = resolve; });
	const now = new Date().toISOString();
    const createdAt = now;
	await page.route(`**/api/v1/sessions/${sessionId}/**`, async (route) => {
		if (new URL(route.request().url()).pathname.endsWith("/conversation")) {
			if (hold) { requestBlocked(); await waiting; }
			await route.fulfill({ json: {
				conversationId: "conversation", sessionId, harness: "codex", mode: "chat", controller: "busy",
				activeBranchId: "branch", liveGeneration: "generation", liveSequence: saved ? chunks.length : 0,
				latestSequence: saved ? 2 : 1, oldestSequence: 1, hasMoreBefore: false, settings: {}, activities: [],
				turns: [{ id: "turn", providerTurnId: "provider-turn", state: saved ? "completed" : "running", requestedAt: now }],
				messages: [
					{ kind: "message", id: "prompt", turnId: "turn", sequence: 1, revision: 1, role: "user", origin: "human", text: "Show the reply as it arrives.", streaming: false, createdAt: now },
					...(saved ? [{ kind: "message", id: "saved-reply", providerItemId: "reply", turnId: "turn", sequence: 2, revision: 1, role: "assistant", origin: "provider", text: reply, streaming: false, createdAt: now }] : []),
				],
			} });
		} else await route.fulfill({ json: {} });
	});
	await page.route("**/*.woff2*", async (route) => {
        const name = new URL(route.request().url()).pathname.split("/").pop();
        if (name?.startsWith("geist")) {
            const family = name.startsWith("geist-mono") ? "geist-mono" : "geist";
            await route.fulfill({path: `/Users/dhruvsharma/.codex/worktrees/5522/agent-orchestrator-1/frontend/node_modules/@fontsource-variable/${family}/files/${name}`, contentType:"font/woff2"});
        } else await route.continue();
    });
    await page.goto(`/#/projects/fake-proj/sessions/${sessionId}`);
	const log = page.getByRole("log", { name: "Conversation" });
	await expect(log.getByText("Show the reply as it arrives.")).toBeVisible({ timeout: 15_000 });

    await page.evaluate(async () => { await document.fonts.ready; });
    const label = async (phase: string) => page.evaluate(({mode, phase}) => {
        let banner = document.getElementById("evidence-label");
        if (!banner) {
            banner = document.createElement("div"); banner.id="evidence-label";
            banner.style.cssText="position:fixed;top:0;left:0;right:0;z-index:999999;background:#11283e;color:#fff;padding:12px 20px;font:15px sans-serif";
            document.body.append(banner);
        }
        banner.textContent = `${mode} | Actual renderer; simulated provider + saved-history API | ${phase}`;
    }, {mode,phase});
    await label("Ready — same six reply chunks in both recordings");
    // These pauses pace the fixture for a readable video, never synchronization
    // or production latency. Every behavioral boundary below has an assertion.
    await pauseForRecording(900);
    hold = true;
    await page.evaluate(() => (window as unknown as { requestSavedText: () => void }).requestSavedText());
    await blocked;
    let received = "";
    for (const [index, delta] of chunks.entries()) {
        received += delta;
        await label(`Saving PAUSED (test only) | incoming chunks ${index+1}/${chunks.length}`);
        await page.evaluate(({index,delta,createdAt}) => {
            (window as unknown as {sendLiveText?: (frame: unknown) => void}).sendLiveText?.({
                generation:"generation",conversationId:"conversation",branchId:"branch",
                afterSequence:index,resetSequence:0,sequence:index+1,
                events:[{sequence:index+1,kind:"message.delta",providerItemId:"reply",providerTurnId:"provider-turn",delta,createdAt}],
            });
        }, {index,delta,createdAt});
        if (mode === "BEFORE") await expect(log.getByText(received.trim(), {exact:true})).toHaveCount(0);
        else await expect(log.getByText(received.trim(), {exact:true})).toBeVisible();
        observations.push({phase:"saving-held",delivered:index+1,replyVisible:mode!=="BEFORE",saved:false});
        await pauseForRecording(350);
    }
    await label(mode === "BEFORE" ? "All chunks arrived | reply still waiting for saved history" : "All chunks visible | saved history is still held");
    await page.screenshot({path:`${process.env.EVIDENCE_DIR}/${mode.toLowerCase()}-held.png`});
    await pauseForRecording(1400);
    const caughtUp = page.waitForResponse(async (response) => {
        if (!new URL(response.url()).pathname.endsWith("/conversation")) return false;
        try { return (await response.json()).liveSequence === chunks.length; } catch { return false; }
    });
    saved = true;
    release();
    await caughtUp;
    await expect(log.getByText(reply, {exact:true})).toHaveCount(1);
    await label("Saving released | complete reply appears exactly once");
    await pauseForRecording(900);
    // Re-deliver the same frame, as a transient stream reconnect may do.
    await page.evaluate(({chunks,createdAt}) => {
        (window as unknown as {sendLiveText?: (frame: unknown) => void}).sendLiveText?.({
            generation:"generation",conversationId:"conversation",branchId:"branch",afterSequence:0,resetSequence:0,sequence:chunks.length,
            events:chunks.map((delta,index)=>({sequence:index+1,kind:"message.delta",providerItemId:"reply",providerTurnId:"provider-turn",delta,createdAt})),
        });
    },{chunks,createdAt});
    await expect(log.getByText(reply, {exact:true})).toHaveCount(1);
    observations.push({phase:"saved-and-replayed",delivered:chunks.length,replyVisible:true,saved:true});
    await label(mode === "AFTER" ? "Saved + repeated stream frame | still one complete reply" : "Saved history received | one complete reply");
    await page.screenshot({path:`${process.env.EVIDENCE_DIR}/${mode.toLowerCase()}-saved.png`});
    await pauseForRecording(1700);
    await writeFile(`${process.env.EVIDENCE_DIR}/${mode.toLowerCase()}-assertions.json`, JSON.stringify({mode,commit:process.env.EVIDENCE_COMMIT,capturedAt:new Date().toISOString(),reply,observations},null,2)+"\n");
});
