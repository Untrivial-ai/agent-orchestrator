import { Feather } from "@expo/vector-icons";
import * as DocumentPicker from "expo-document-picker";
import * as ImagePicker from "expo-image-picker";
import { useState } from "react";
import { ActivityIndicator, Image, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { ChatAttachmentMenu } from "./ChatAttachmentMenu";
import type { ChatImage, ChatResource } from "./types";

type Attachment =
	| { id: string; kind: "image"; name: string; bytes: number; image: ChatImage }
	| { id: string; kind: "resource"; name: string; resource: ChatResource };

const MAX_ATTACHMENTS = 8;
const MAX_FILE_BYTES = 500_000;
const MAX_IMAGE_BYTES = 10 * 1024 * 1024;
const MAX_IMAGE_BYTES_TOTAL = 25 * 1024 * 1024;
const IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp", "image/bmp"]);

/** Reviewer-only composer: send/attachments/interrupt, deliberately no session actions. */
export function ReviewerComposer({ busy, stopped, onSend, onInterrupt }: {
	busy: boolean;
	stopped: boolean;
	onSend(text: string, images?: ChatImage[], resources?: ChatResource[]): Promise<void>;
	onInterrupt(): Promise<void>;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [text, setText] = useState("");
	const [attachments, setAttachments] = useState<Attachment[]>([]);
	const [submitting, setSubmitting] = useState(false);
	const [error, setError] = useState("");

	const addPhoto = async () => {
		setError("");
		try {
			const result = await ImagePicker.launchImageLibraryAsync({ mediaTypes: ["images"], base64: true, quality: 0.82, allowsMultipleSelection: true, selectionLimit: 4 });
			if (result.canceled) return;
			const errors = new Set<string>();
			const next = result.assets.flatMap((asset): Attachment[] => {
				const mimeType = (asset.mimeType || "image/jpeg").toLowerCase();
				if (!asset.base64) { errors.add("Some images couldn't be read and were skipped."); return []; }
				if (!IMAGE_TYPES.has(mimeType)) { errors.add("Only PNG, JPEG, GIF, WebP, and BMP images are supported."); return []; }
				const bytes = asset.fileSize ?? Math.floor(asset.base64.length * 0.75);
				if (bytes > MAX_IMAGE_BYTES) { errors.add("Each image must be under 10 MB."); return []; }
				return [{ id: `${asset.assetId || asset.uri}-${Date.now()}`, kind: "image", name: asset.fileName || "Image", bytes, image: { mimeType, data: asset.base64 } }];
			});
			const accepted = [...attachments];
			let imageBytes = accepted.reduce((sum, item) => sum + (item.kind === "image" ? item.bytes : 0), 0);
			for (const item of next) {
				if (accepted.length >= MAX_ATTACHMENTS) { errors.add(`You can attach up to ${MAX_ATTACHMENTS} items.`); break; }
				if (item.kind === "image" && imageBytes + item.bytes > MAX_IMAGE_BYTES_TOTAL) { errors.add("Images must total under 25 MB."); break; }
				accepted.push(item);
				if (item.kind === "image") imageBytes += item.bytes;
			}
			setAttachments(accepted);
			if (errors.size) setError([...errors].join(" "));
		} catch (cause) { setError(cause instanceof Error ? cause.message : "Could not attach this photo."); }
	};

	const addFile = async () => {
		setError("");
		try {
			const result = await DocumentPicker.getDocumentAsync({ multiple: true, copyToCacheDirectory: true, type: ["text/*", "application/json", "application/xml", "application/yaml"] });
			if (result.canceled) return;
			const added: Attachment[] = [];
			for (const asset of result.assets) {
				if (attachments.length + added.length >= MAX_ATTACHMENTS) throw new Error(`You can attach up to ${MAX_ATTACHMENTS} items.`);
				if ((asset.size || 0) > MAX_FILE_BYTES) throw new Error(`${asset.name} is larger than 500 KB.`);
				const body = await fetch(asset.uri).then((response) => response.text());
				if (new TextEncoder().encode(body).byteLength > MAX_FILE_BYTES) throw new Error(`${asset.name} is larger than 500 KB.`);
				added.push({ id: `${asset.uri}-${Date.now()}`, kind: "resource", name: asset.name, resource: { uri: `mobile-attachment://${encodeURIComponent(asset.name)}`, name: asset.name, mimeType: asset.mimeType || "text/plain", text: body } });
			}
			setAttachments((current) => [...current, ...added]);
		} catch (cause) { setError(cause instanceof Error ? cause.message : "Could not attach this file."); }
	};

	const submit = async () => {
		if (submitting || stopped || (!text.trim() && !attachments.length)) return;
		setSubmitting(true); setError("");
		try {
			await onSend(text.trim(), attachments.flatMap((item) => item.kind === "image" ? [item.image] : []), attachments.flatMap((item) => item.kind === "resource" ? [item.resource] : []));
			setText(""); setAttachments([]); haptics.success();
		} catch (cause) { setError(cause instanceof Error ? cause.message : "Could not send this reply."); }
		finally { setSubmitting(false); }
	};

	return <View style={styles.dock}>
		{attachments.length ? <ScrollView horizontal contentContainerStyle={styles.attachments}>{attachments.map((item) => <View key={item.id} style={styles.attachment}>{item.kind === "image" ? <Image source={{ uri: `data:${item.image.mimeType};base64,${item.image.data}` }} style={styles.image} /> : <Feather name="file-text" size={13} color={t.blue} />}<Text numberOfLines={1} style={styles.name}>{item.name}</Text><Pressable accessibilityLabel={`Remove ${item.name}`} onPress={() => setAttachments((current) => current.filter((candidate) => candidate.id !== item.id))}><Feather name="x" size={13} color={t.textTertiary} /></Pressable></View>)}</ScrollView> : null}
		{error ? <Text accessibilityRole="alert" style={styles.error}>{error}</Text> : null}
		<View style={styles.row}>
			<ChatAttachmentMenu disabled={stopped || submitting} canAttachFile onChoosePhoto={() => void addPhoto()} onChooseFile={() => void addFile()} />
			<TextInput accessibilityLabel="Reply to reviewer" value={text} onChangeText={setText} placeholder={stopped ? "Reviewer is stopped" : "Reply to reviewer…"} placeholderTextColor={t.textTertiary} multiline editable={!stopped && !submitting} style={styles.input} />
			{busy ? <Pressable accessibilityRole="button" accessibilityLabel="Stop reviewer" onPress={() => void onInterrupt().catch((cause) => setError(cause instanceof Error ? cause.message : "Could not stop the reviewer."))} style={styles.stop}><Feather name="square" size={14} color={t.red} /></Pressable> : <Pressable accessibilityRole="button" accessibilityLabel="Send reply" disabled={stopped || submitting || (!text.trim() && !attachments.length)} onPress={() => void submit()} style={[styles.send, (stopped || submitting || (!text.trim() && !attachments.length)) && styles.disabled]}>{submitting ? <ActivityIndicator size="small" color="#fff" /> : <Feather name="arrow-up" size={18} color="#fff" />}</Pressable>}
		</View>
	</View>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	dock: { padding: 12, gap: 8, borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle, backgroundColor: t.bgSurface },
	row: { flexDirection: "row", alignItems: "flex-end", gap: 8 },
	input: { flex: 1, minHeight: 42, maxHeight: 120, color: t.textPrimary, backgroundColor: t.bgElevated, borderRadius: 18, paddingHorizontal: 14, paddingVertical: 10, fontSize: 15 },
	attachments: { gap: 7 }, attachment: { flexDirection: "row", alignItems: "center", gap: 6, maxWidth: 190, backgroundColor: t.bgElevated, borderRadius: 9, padding: 6 }, image: { width: 25, height: 25, borderRadius: 5 }, name: { maxWidth: 120, color: t.textSecondary, fontSize: 12 },
	error: { color: t.red, fontSize: 12 }, stop: { width: 44, height: 44, borderRadius: 22, alignItems: "center", justifyContent: "center", backgroundColor: t.tintRed }, send: { width: 44, height: 44, borderRadius: 22, alignItems: "center", justifyContent: "center", backgroundColor: t.blue }, disabled: { opacity: 0.4 },
});
