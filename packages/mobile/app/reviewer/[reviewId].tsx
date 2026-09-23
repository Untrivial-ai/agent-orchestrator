import { Feather } from "@expo/vector-icons";
import { useLocalSearchParams, useNavigation } from "expo-router";
import { useLayoutEffect, useState } from "react";
import { ActivityIndicator, KeyboardAvoidingView, Platform, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { ChatTimeline } from "../../lib/chat/ChatTimeline";
import { useMobileConversation } from "../../lib/chat/useConversation";
import { haptics } from "../../lib/haptics";
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
	const conversation = useMobileConversation(config, reviewId, { reviewId, eventSessionId: sessionId });
	const [text, setText] = useState("");
	const [sending, setSending] = useState(false);
	const [sendError, setSendError] = useState("");

	useLayoutEffect(() => navigation.setOptions({ title: title ? `Review · ${title}` : "Reviewer chat" }), [navigation, title]);

	if (conversation.loading && !conversation.snapshot) return <View style={styles.center}><ActivityIndicator color={t.blue} /></View>;
	if (!conversation.snapshot) return <EmptyState icon="message-circle" title="Reviewer chat unavailable" message={conversation.unavailable?.message || conversation.error || "The reviewer conversation has not started yet."} action={<Button title="Try again" icon="refresh-cw" variant="ghost" onPress={() => void conversation.refresh()} />} />;

	const send = async () => {
		const message = text.trim();
		if (!message || sending) return;
		haptics.tap();
		setSending(true);
		setSendError("");
		try {
			await conversation.send(message);
			setText("");
		} catch (value) {
			setSendError(value instanceof Error ? value.message : "Could not send this reply.");
		} finally {
			setSending(false);
		}
	};

	return <KeyboardAvoidingView style={styles.screen} behavior={Platform.OS === "ios" ? "padding" : undefined} keyboardVerticalOffset={88}>
		{conversation.unavailable?.message || conversation.error || conversation.actionError || sendError ? <Text accessibilityRole="alert" style={styles.error}>{conversation.unavailable?.message || conversation.error || conversation.actionError || sendError}</Text> : null}
		<ChatTimeline snapshot={conversation.snapshot} loadingOlder={conversation.loadingOlder} onLoadOlder={() => void conversation.loadOlder()} approvalPending={conversation.pendingActions.includes("approval")} inputPending={conversation.pendingActions.includes("input")} onDecide={conversation.resolveApproval} onResolveInput={conversation.resolveInput} onRollback={async () => 0} />
		<View style={styles.composer}>
			<TextInput accessibilityLabel="Reply to reviewer" value={text} onChangeText={setText} placeholder="Reply to reviewer…" placeholderTextColor={t.textTertiary} multiline style={styles.input} editable={!sending && !conversation.unavailable} />
			{conversation.snapshot.controller.state === "busy" ? <Pressable accessibilityRole="button" accessibilityLabel="Stop reviewer" onPress={() => void conversation.interrupt().catch(() => {})} style={styles.stop}><Feather name="square" size={14} color={t.red} /></Pressable> : null}
			<Pressable accessibilityRole="button" accessibilityLabel="Send reply" disabled={!text.trim() || sending || Boolean(conversation.unavailable)} onPress={() => void send()} style={[styles.send, (!text.trim() || sending || conversation.unavailable) && styles.disabled]}>{sending ? <ActivityIndicator size="small" color="#fff" /> : <Feather name="arrow-up" size={18} color="#fff" />}</Pressable>
		</View>
	</KeyboardAvoidingView>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	center: { flex: 1, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	error: { color: t.red, fontSize: 12, paddingHorizontal: 16, paddingVertical: 8, backgroundColor: t.tintRed },
	composer: { flexDirection: "row", alignItems: "flex-end", gap: 8, padding: 12, borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle, backgroundColor: t.bgSurface },
	input: { flex: 1, minHeight: 42, maxHeight: 120, color: t.textPrimary, backgroundColor: t.bgElevated, borderRadius: 18, paddingHorizontal: 14, paddingVertical: 10, fontSize: 15 },
	stop: { width: 44, height: 44, borderRadius: 22, alignItems: "center", justifyContent: "center", backgroundColor: t.tintRed },
	send: { width: 44, height: 44, borderRadius: 22, alignItems: "center", justifyContent: "center", backgroundColor: t.blue },
	disabled: { opacity: 0.4 },
});
