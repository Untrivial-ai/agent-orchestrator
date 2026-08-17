import { useCallback, useEffect, useRef, useState } from "react";
import type { ServerConfig } from "../config";
import {
	acknowledgeSessionInterfaceTransitionNotice,
	cancelSessionInterfaceTransition,
	getSessionInterfaceTransition,
	startSessionInterfaceTransition,
	type SessionInterfaceTransitionStatus,
} from "../chat/api";
import {
	interfaceTransitionPollInterval,
	mobileInterfaceTransitionIsActive,
} from "./interfaceTransition";

export {
	mobileInterfaceTransitionIsActive,
	mobileInterfaceTransitionIsCancellable,
} from "./interfaceTransition";

export function useInterfaceTransition(
	cfg: ServerConfig | null,
	sessionId: string,
	onSettled?: () => void | Promise<void>,
) {
	const [status, setStatus] = useState<SessionInterfaceTransitionStatus>();
	const [loading, setLoading] = useState(Boolean(cfg && sessionId));
	const [starting, setStarting] = useState(false);
	const [cancelling, setCancelling] = useState(false);
	const [acknowledgingNotice, setAcknowledgingNotice] = useState(false);
	const [acknowledgeNoticeError, setAcknowledgeNoticeError] = useState<string>();
	const [error, setError] = useState<string>();
	const settledRef = useRef("");
	const onSettledRef = useRef(onSettled);
	onSettledRef.current = onSettled;

	const refresh = useCallback(async () => {
		if (!cfg || !sessionId) return;
		try {
			const next = await getSessionInterfaceTransition(cfg, sessionId);
			setStatus(next);
			setError(undefined);
			const transition = next.transition;
			if (transition && !mobileInterfaceTransitionIsActive(transition) && settledRef.current !== transition.id) {
				settledRef.current = transition.id;
				await onSettledRef.current?.();
			}
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			setLoading(false);
		}
	}, [cfg, sessionId]);

	useEffect(() => {
		void refresh();
	}, [refresh]);

	useEffect(() => {
		const interval = interfaceTransitionPollInterval(status?.transition);
		if (!cfg || !sessionId || interval === undefined) return;
		const timer = setInterval(() => void refresh(), interval);
		return () => clearInterval(timer);
	}, [cfg, refresh, sessionId, status?.transition?.phase]);

	const start = useCallback(
		async (targetMode: "chat" | "tui", policy: "drain" | "interrupt") => {
			if (!cfg) throw new Error("No AO server configured");
			setStarting(true);
			setError(undefined);
			try {
				const transition = await startSessionInterfaceTransition(cfg, sessionId, targetMode, policy);
				setStatus((current) => ({
					supported: current?.supported ?? true,
					targetMode,
					transition,
				}));
			} catch (cause) {
				const message = cause instanceof Error ? cause.message : String(cause);
				setError(message);
				throw cause;
			} finally {
				setStarting(false);
			}
		},
		[cfg, sessionId],
	);

	const cancel = useCallback(async () => {
		if (!cfg) throw new Error("No AO server configured");
		setCancelling(true);
		setError(undefined);
		try {
			await cancelSessionInterfaceTransition(cfg, sessionId);
			await refresh();
		} catch (cause) {
			const message = cause instanceof Error ? cause.message : String(cause);
			setError(message);
			throw cause;
		} finally {
			setCancelling(false);
		}
	}, [cfg, refresh, sessionId]);

	const acknowledgeNotice = useCallback(
		async (transitionId: string) => {
			if (!cfg) throw new Error("No AO server configured");
			setAcknowledgingNotice(true);
			setAcknowledgeNoticeError(undefined);
			try {
				const transition = await acknowledgeSessionInterfaceTransitionNotice(
					cfg,
					sessionId,
					transitionId,
				);
				setStatus((current) =>
					current?.transition?.id === transition.id ? { ...current, transition } : current,
				);
			} catch (cause) {
				const message = cause instanceof Error ? cause.message : String(cause);
				setAcknowledgeNoticeError(message);
				throw cause;
			} finally {
				setAcknowledgingNotice(false);
			}
		},
		[cfg, sessionId],
	);

	return {
		status,
		transition: status?.transition,
		loading,
		starting,
		cancelling,
		acknowledgingNotice,
		error,
		acknowledgeNoticeError,
		start,
		cancel,
		acknowledgeNotice,
		refresh,
	};
}
