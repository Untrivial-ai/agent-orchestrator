// Public API for the in-product survey. The survey opens only when the user
// clicks the sidebar "Help shape AO" invite; answers route through the
// renderer's own captureRendererEvent, so they ride the same batched, sanitized
// pipeline as every other event and cost nothing extra.

import { captureRendererEvent } from "../telemetry";
import { SurveyController } from "./surveyController";

const controller = new SurveyController({
	storage: typeof window !== "undefined" ? window.localStorage : undefined,
	capture: (event, properties) => void captureRendererEvent(event, properties),
});

let open = false;
const listeners = new Set<() => void>();
const emit = () => {
	for (const l of listeners) l();
};

/** Subscribe the prompt component and the sidebar invite to changes. */
export function subscribeSurvey(listener: () => void): () => void {
	listeners.add(listener);
	return () => {
		listeners.delete(listener);
	};
}

/** Whether the survey modal is currently open. */
export function isSurveyOpen(): boolean {
	return open;
}

// Decide once per app session whether to surface the invite, so it shows at
// varied moments rather than on every launch.
let sessionInviteRoll: boolean | null = null;
const INVITE_SESSION_CHANCE = 0.5;

/** Whether to show the "Help shape AO" invite in the sidebar right now. */
export function surveyInviteVisible(): boolean {
	if (!controller.inviteEligible()) return false;
	if (sessionInviteRoll === null) sessionInviteRoll = Math.random() < INVITE_SESSION_CHANCE;
	return sessionInviteRoll;
}

/** User crossed the invite: hush it for 48 hours. */
export function dismissInvite(): void {
	controller.dismissInvite();
	emit();
}

/** User chose "don't show again": retire the invite for good. */
export function optOutOfInvite(): void {
	controller.optOut();
	emit();
}

/** User opened the survey from the sidebar invite. */
export function openSurvey(): void {
	if (open) return;
	open = true;
	emit();
}

/** Record one question's answer (single choice, multi list, or text). */
export function recordAnswer(id: string, value: string | string[]): void {
	controller.answer(id, value);
}

/** User finished the survey: record completion (never invite again) and close. */
export function completeSurvey(): void {
	controller.markCompleted();
	open = false;
	emit();
}

/** User closed the survey without finishing. */
export function closeSurvey(): void {
	open = false;
	emit();
}
