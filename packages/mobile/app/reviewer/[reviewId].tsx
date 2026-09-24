import { useLocalSearchParams, useNavigation } from "expo-router";
import { useLayoutEffect } from "react";
import { ActivityIndicator, KeyboardAvoidingView, Platform, StyleSheet, Text, View } from "react-native";
import { ReviewerComposer } from "../../lib/chat/ReviewerComposer";
import { ChatTimeline } from "../../lib/chat/ChatTimeline";
import { useMobileConversation } from "../../lib/chat/useConversation";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { Button, EmptyState } from "../../lib/ui";

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

export default function ReviewerConversationScreen() {
	const { reviewId = "", sessionId = "", title } = useLocalSearchParams<{ reviewId: string; sessionId?: string; title?: string }>();
	const navigation = useNavigation();
	const { config } = useApp();
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	// The review id owns the conversation routes; the worker id owns attachment
	// staging and SSE refresh. Never stage reviewer attachments under a review id.
	const conversation = useMobileConversation(config, sessionId || reviewId, { reviewId, eventSessionId: sessionId });

	useLayoutEffect(() => navigation.setOptions({ title: title ? `Review · ${title}` : "Reviewer chat" }), [navigation, title]);

	if (conversation.loading && !conversation.snapshot) return <View style={styles.center}><ActivityIndicator color={t.accent} /></View>;
	if (!conversation.snapshot) return <EmptyState icon="message-circle" title="Reviewer chat unavailable" message={conversation.unavailable?.message || conversation.error || "The reviewer conversation has not started yet."} action={<Button title="Try again" icon="refresh-cw" variant="ghost" onPress={() => void conversation.refresh()} />} />;

	return <KeyboardAvoidingView style={styles.screen} behavior={Platform.OS === "ios" ? "padding" : undefined} keyboardVerticalOffset={88}>
		{conversation.unavailable?.message || conversation.error || conversation.actionError ? <Text accessibilityRole="alert" style={styles.error}>{conversation.unavailable?.message || conversation.error || conversation.actionError}</Text> : null}
		<ChatTimeline snapshot={conversation.snapshot} loadingOlder={conversation.loadingOlder} onLoadOlder={() => void conversation.loadOlder()} approvalPending={conversation.pendingActions.includes("approval")} inputPending={conversation.pendingActions.includes("input")} onDecide={conversation.resolveApproval} onResolveInput={conversation.resolveInput} />
		<ReviewerComposer busy={conversation.snapshot.controller.state === "busy"} stopped={conversation.snapshot.controller.state === "stopped" || Boolean(conversation.unavailable)} onSend={conversation.send} onInterrupt={conversation.interrupt} />
	</KeyboardAvoidingView>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	center: { flex: 1, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	error: { color: t.red, fontSize: 12, paddingHorizontal: 16, paddingVertical: 8, backgroundColor: t.tintRed },
});
