// Synthetic events exercise the real Cloud projection and shared renderer.
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { appI18n } from "../../src/renderer/i18n";
import { TooltipProvider } from "../../src/renderer/components/ui/tooltip";
import { ChatWorkspace } from "../../src/renderer/components/chat/ChatWorkspace";
import { toSnapshot } from "../../src/renderer/components/chat/CloudSessionChatSurface";
import type { WorkspaceSession } from "../../src/renderer/types/workspace";
import type { CloudCpClientEvent } from "../../src/renderer/lib/cloud-cp";
import "../../src/renderer/styles.css";

const session = {
 id:"cloud-parity-fixture", workspaceId:"fixture-project", workspaceName:"Fixture",
 title:"Synthetic home search", provider:"codex", kind:"worker", mode:"chat", status:"idle",
 updatedAt:"2026-10-01T00:00:00Z", prs:[], cloud:{orgId:"fixture-org"},
} satisfies WorkspaceSession;
const event = (sequence:number, type:string, payload:object):CloudCpClientEvent => ({
 sessionId:session.id, sequence, type, payload, createdAt:`2026-10-01T00:00:${String(sequence).padStart(2,"0")}Z`,
});
const snapshot = toSnapshot(session,[
 event(1,"chat.user_message",{turnId:"search",text:"Compare two homes."}),
 event(2,"chat.turn_started",{turnId:"search",attempt:1}),
 event(3,"chat.assistant_delta",{turnId:"search",attempt:1,itemId:"progress",text:"Checking listings."}),
 event(4,"chat.assistant_delta",{turnId:"search",attempt:1,itemId:"answer",text:"## Comparison"}),
 event(5,"chat.assistant_delta",{turnId:"search",attempt:1,itemId:"answer",text:"\n\n"}),
 event(6,"chat.assistant_delta",{turnId:"search",attempt:1,itemId:"answer",text:"Both homes meet the requested criteria."}),
 event(7,"chat.turn_completed",{turnId:"search"}),
 event(8,"chat.user_message",{turnId:"report",origin:"automation",senderSessionId:"fixture-worker",text:"Worker report: the listing comparison is complete."}),
 event(9,"chat.turn_started",{turnId:"report",attempt:1}),
 event(10,"chat.assistant_delta",{turnId:"report",attempt:1,itemId:"acknowledgment",text:"Recorded the comparison."}),
 event(11,"chat.turn_completed",{turnId:"report"}),
 event(12,"chat.user_message",{turnId:"commute",text:"Compare commute times."}),
 event(13,"chat.turn_started",{turnId:"commute",attempt:1}),
 event(14,"chat.assistant_delta",{turnId:"commute",attempt:1,itemId:"answer",text:"The second home has a shorter commute."}),
 event(15,"chat.turn_completed",{turnId:"commute"}),
]);
createRoot(document.getElementById("parity-root")!).render(
 <QueryClientProvider client={new QueryClient()}><I18nextProvider i18n={appI18n}><TooltipProvider>
  <ChatWorkspace snapshot={snapshot} session={session} newWorkDisabled />
 </TooltipProvider></I18nextProvider></QueryClientProvider>,
);
