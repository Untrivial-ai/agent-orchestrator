import { create } from "zustand";
import type { CloudCpProjectCoderConfig, CloudCpSessionRepo } from "../lib/cloud-cp/types";

export type CoderSize = "small" | "medium" | "large";

// The per-session Coder picker choices (template + curated form). Kept in memory
// only — these are per-session choices, not a persisted preference, and reset
// when the composer clears. "" templateId means the deployment default template,
// which sends no picker options and preserves the pre-existing behavior.
export interface CoderSessionOptionsState {
	templateId: string;
	// The parameter names the chosen template declares, tracked alongside the id
	// so size/startup are only ever offered and sent when the template accepts
	// them (sending an undeclared rich parameter makes Coder reject the build).
	supportedParams: string[];
	size: CoderSize;
	startupScript: string;
	extraRepos: CloudCpSessionRepo[];
	setTemplate: (templateId: string, supportedParams: string[]) => void;
	setSize: (size: CoderSize) => void;
	setStartupScript: (startupScript: string) => void;
	setExtraRepos: (extraRepos: CloudCpSessionRepo[]) => void;
	reset: () => void;
}

const initialState = {
	templateId: "",
	supportedParams: [] as string[],
	size: "medium" as CoderSize,
	startupScript: "",
	extraRepos: [] as CloudCpSessionRepo[],
};

export const useCoderSessionOptionsStore = create<CoderSessionOptionsState>((set) => ({
	...initialState,
	setTemplate: (templateId, supportedParams) => set({ templateId, supportedParams }),
	setSize: (size) => set({ size }),
	setStartupScript: (startupScript) => set({ startupScript }),
	setExtraRepos: (extraRepos) => set({ extraRepos }),
	reset: () => set({ ...initialState, supportedParams: [], extraRepos: [] }),
}));

// buildCoderRequestOptions turns the picker state into the createProject `coder`
// payload, or undefined when the choice is "Default with no extra repos" — in
// which case the request omits `coder` entirely and the project behaves exactly
// as before. Size and startup are only sent alongside a chosen (non-default)
// template, mirroring the control plane's validation.
export function buildCoderRequestOptions(state: {
	templateId: string;
	supportedParams: string[];
	size: CoderSize;
	startupScript: string;
	extraRepos: CloudCpSessionRepo[];
}): CloudCpProjectCoderConfig | undefined {
	const templateId = state.templateId.trim();
	const extraRepos = state.extraRepos
		.map((repo) => ({ url: repo.url.trim(), branch: repo.branch?.trim() || undefined }))
		.filter((repo) => repo.url.length > 0);
	if (!templateId && extraRepos.length === 0) return undefined;
	const coder: CloudCpProjectCoderConfig = {};
	if (templateId) {
		coder.templateId = templateId;
		// Only send a rich parameter the template actually declares.
		if (state.supportedParams.includes("size")) coder.size = state.size;
		if (state.supportedParams.includes("startup_script") && state.startupScript.trim().length > 0) {
			coder.startupScript = state.startupScript;
		}
	}
	if (extraRepos.length > 0) coder.extraRepos = extraRepos;
	return coder;
}
