import { useLocalSearchParams, useNavigation } from "expo-router";
import { useLayoutEffect } from "react";
import { ActivityIndicator, KeyboardAvoidingView, Platform, StyleSheet, Text, View } from "react-native";
import { ReviewerComposer } from "../../lib/chat/ReviewerComposer";
import { ChatTimeline } from "../../lib/chat/ChatTimeline";
import { useMobileConversation } from "../../lib/chat/useConversation";
import { hostRouteMatches } from "../../lib/hostRoute";
import { HostScope, useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { Button, EmptyState } from "../../lib/ui";

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

export default function ReviewerConversationScreen() {
	const { hostId } = useLocalSearchParams<{ hostId?: string }>();
	return hostId ? <HostScope key={hostId} hostId={hostId}><ReviewerConversationContent /></HostScope> : <ReviewerConversationContent />;
}

function ReviewerConversationContent() {
	const { reviewId = "", sessionId = "", title, hostId: routeHostId } = useLocalSearchParams<{ reviewId: string; sessionId?: string; title?: string; hostId?: string }>();
	const workerSessionId = sessionId.trim();
	const navigation = useNavigation();
	const { config, currentHostId } = useApp();
	const hostMatches = hostRouteMatches(routeHostId, currentHostId);
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	// The review id owns the conversation routes; the worker id owns attachment
	// staging and SSE refresh. Never stage reviewer attachments under a review id.
	const conversation = useMobileConversation(hostMatches ? config : null, workerSessionId, { reviewId, eventSessionId: workerSessionId });

	useLayoutEffect(() => navigation.setOptions({ title: title ? `Review · ${title}` : "Reviewer chat" }), [navigation, title]);

	if (!hostMatches) return <EmptyState icon="message-circle" title="Review belongs to another machine" message="Open it from that machine's session." />;
	if (conversation.loading && !conversation.snapshot) return <View style={styles.center}><ActivityIndicator color={t.accent} /></View>;
	if (!conversation.snapshot) return <EmptyState icon="message-circle" title="Reviewer chat unavailable" message={conversation.unavailable?.message || conversation.error || "The reviewer conversation has not started yet."} action={<Button title="Try again" icon="refresh-cw" variant="ghost" onPress={() => void conversation.refresh()} />} />;

	return <KeyboardAvoidingView style={styles.screen} behavior={Platform.OS === "ios" ? "padding" : undefined} keyboardVerticalOffset={88}>
		{conversation.unavailable?.message || conversation.error || conversation.actionError ? <Text accessibilityRole="alert" style={styles.error}>{conversation.unavailable?.message || conversation.error || conversation.actionError}</Text> : null}
		{conversation.pendingSends.map((pendingSend) => pendingSend.state === "failed" ? <View key={pendingSend.id} style={styles.pending}><Text accessibilityRole="alert" style={styles.error}>{pendingSend.error || "Message delivery uncertain"}</Text><Button title={pendingSend.restored && pendingSend.hasAttachments ? "Refresh history" : "Retry"} onPress={() => void (pendingSend.restored && pendingSend.hasAttachments ? conversation.refresh() : conversation.retrySend(pendingSend.id)).catch(() => {})} /><Button title="Discard" variant="ghost" onPress={() => void conversation.discardSend(pendingSend.id).catch(() => {})} /></View> : null)}
		<ChatTimeline snapshot={conversation.snapshot} loadingOlder={conversation.loadingOlder} onLoadOlder={() => void conversation.loadOlder()} approvalPending={conversation.pendingActions.includes("approval")} inputPending={conversation.pendingActions.includes("input")} onDecide={conversation.resolveApproval} onResolveInput={conversation.resolveInput} />
		<ReviewerComposer attachmentsEnabled={Boolean(workerSessionId)} busy={conversation.snapshot.controller.state === "busy"} stopped={conversation.snapshot.controller.state === "stopped" || Boolean(conversation.unavailable)} onSend={conversation.send} onAcknowledgeSend={conversation.acknowledgeSend} completedRetry={conversation.completedRetry} onInterrupt={conversation.interrupt} />
	</KeyboardAvoidingView>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	center: { flex: 1, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	error: { color: t.red, fontSize: 12, paddingHorizontal: 16, paddingVertical: 8, backgroundColor: t.tintRed },
	pending: { flexDirection: "row", alignItems: "center", flexWrap: "wrap" },
});
