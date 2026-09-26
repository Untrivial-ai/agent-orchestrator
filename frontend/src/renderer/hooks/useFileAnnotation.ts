import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatFileAnnotationMessage } from "../../shared/file-annotations";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import type { ActiveFileAnnotationTarget, FileAnnotationModel, FileAnnotationStatus } from "../components/WorkspaceDiffView";

function isSameAnnotationTarget(current: ActiveFileAnnotationTarget | null, next: ActiveFileAnnotationTarget): boolean {
	return current?.path === next.path
		&& current.side === next.side
		&& current.line === next.line
		&& current.scope === next.scope
		&& current.surface === next.surface;
}

type UseFileAnnotationOptions = {
	source?: string;
	sendMessage?: (message: string) => Promise<void>;
};

export function useFileAnnotation(sessionId: string, options: UseFileAnnotationOptions = {}): FileAnnotationModel {
	const { source, sendMessage } = options;
	const { t } = useTranslation();
	const [target, setTarget] = useState<ActiveFileAnnotationTarget | null>(null);
	const [draft, setDraft] = useState("");
	const [status, setStatus] = useState<FileAnnotationStatus>("idle");
	const [error, setError] = useState("");
	const generationRef = useRef(0);
	const sentTimerRef = useRef<number | null>(null);

	const cancel = () => {
		generationRef.current += 1;
		setTarget(null);
		setDraft("");
		setStatus("idle");
		setError("");
	};

	useEffect(() => {
		cancel();
	}, [sessionId, source]);
	useEffect(
		() => () => {
			if (sentTimerRef.current !== null) window.clearTimeout(sentTimerRef.current);
		},
		[],
	);

	const begin = (nextTarget: ActiveFileAnnotationTarget) => {
		if (isSameAnnotationTarget(target, nextTarget)) {
			cancel();
			return;
		}
		generationRef.current += 1;
		if (sentTimerRef.current !== null) window.clearTimeout(sentTimerRef.current);
		sentTimerRef.current = null;
		setTarget({ ...nextTarget, source });
		setDraft("");
		setStatus("idle");
		setError("");
	};
	const submit = async (text = draft) => {
		if (!target || !text.trim() || status === "sending") return;
		const generation = generationRef.current;
		setDraft(text);
		setStatus("sending");
		setError("");
		try {
			const message = formatFileAnnotationMessage(target, text);
			if (sendMessage) {
				await sendMessage(message);
			} else {
				const { error: responseError } = await apiClient.POST("/api/v1/sessions/{sessionId}/send", {
					params: { path: { sessionId } },
					body: { message },
				});
				if (responseError) throw new Error(apiErrorMessage(responseError, t("files.feedbackError")));
			}
			if (generation !== generationRef.current) return;
			setStatus("sent");
			sentTimerRef.current = window.setTimeout(() => {
				sentTimerRef.current = null;
				cancel();
			}, 1_200);
		} catch (submitError) {
			if (generation !== generationRef.current) return;
			setStatus("error");
			setError(apiErrorMessage(submitError, t("files.feedbackError")));
		}
	};

	return { target, draft, status, error, begin, setDraft, cancel, submit };
}
