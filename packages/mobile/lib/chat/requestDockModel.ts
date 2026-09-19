import { requestPresentation } from "./chatPresentation";
import { elicitationPromptCopy, groupedQuestionInputs, humanizeInputName, inputOptions } from "./elicitationModel";
import { pendingApproval, pendingInput, type ConversationSnapshot, type InputProperty } from "./types";

/**
 * The pending request, shaped so it can take the composer's place.
 *
 * A blocking question is not a notification to go and find — it is the next
 * thing you have to do. So instead of a bar that points at a card further up
 * the timeline, the request becomes the input surface: the composer steps
 * aside, and the question, its options and a free-text row sit where you were
 * about to type anyway.
 *
 * Pure, so what each page asks and whether the card can answer it at all is
 * unit-tested rather than decided mid-render.
 */
export type RequestDockOption = {
	/** Decision id for an approval, enum value for an elicitation. */
	id: string;
	label: string;
	description?: string;
};

export type RequestDockFreeText = {
	/** Schema property the typed answer belongs to. */
	name: string;
	placeholder: string;
	numeric: boolean;
	maxLength?: number;
};

export type RequestDockPage = {
	question: string;
	options: RequestDockOption[];
	/** The "Type your answer…" row, when this question accepts one. */
	freeText?: RequestDockFreeText;
	/** Schema property the options belong to. Absent for an approval. */
	propertyName?: string;
	/** This question takes several answers, so options toggle instead of submit. */
	multi: boolean;
	required: boolean;
};

export type RequestDockModel = {
	kind: "approval" | "input";
	/** "Approval needed" / "Agent needs input". */
	title: string;
	pages: RequestDockPage[];
	requestId?: string;
	/** Where the full card sits, for the cases the dock hands back to it. */
	sequence: number;
	/**
	 * False when the schema is richer than a question with options can express —
	 * booleans, arrays of objects, several fields at once, a URL handshake. The
	 * card then names the request and sends you to the inline form, which owns
	 * the fields and the validation. Better to defer than to answer it wrongly.
	 */
	canAnswerInline: boolean;
	busy: boolean;
};

/** An approval is the cheaper of the two to answer, so it goes first. */
export function requestDockModel(
	snapshot: ConversationSnapshot | undefined,
	pending: { approval: boolean; input: boolean },
): RequestDockModel | null {
	if (!snapshot) return null;

	const approval = pendingApproval(snapshot);
	if (approval) {
		const decisions = approval.decisions ?? [];
		return {
			kind: "approval",
			title: requestPresentation("approval", true).title,
			pages: [{
				// The reason reads as a question; the command is the fallback and is
				// shown verbatim because it is what will actually run.
				question: approval.detail?.reason?.trim() || approval.detail?.command || approval.summary || "",
				options: decisions.map((decision) => ({ id: decision.id, label: decision.label })),
				multi: false,
				required: true,
			}],
			requestId: approval.requestId,
			sequence: approval.sequence,
			// An approval with no decisions cannot be answered from anywhere; the
			// inline card already explains that, so let it.
			canAnswerInline: decisions.length > 0 && Boolean(approval.requestId),
			busy: pending.approval,
		};
	}

	const input = pendingInput(snapshot);
	if (!input) return null;

	const title = requestPresentation("input", true).title;
	const base = {
		kind: "input" as const,
		title,
		requestId: input.requestId,
		sequence: input.sequence,
		busy: pending.input,
	};
	const prompt = elicitationPromptCopy(input.detail?.message || input.detail?.schema?.description || input.summary) ?? "";
	const defer = { ...base, pages: [{ question: prompt, options: [], multi: false, required: true }], canAnswerInline: false };

	// A URL handshake opens a link and is nothing like a question.
	if (input.detail?.inputMode === "url") return defer;
	if (!input.requestId) return defer;

	const properties = Object.entries(input.detail?.schema?.properties ?? {});
	if (!properties.length) return defer;

	const required = input.detail?.schema?.required ?? [];
	// Only the question_N / question_N_custom convention pages cleanly; anything
	// else is a form, and forms belong in the card.
	const groups = groupedQuestionInputs(properties);
	if (!groups) return defer;

	const pages: RequestDockPage[] = [];
	for (const group of groups) {
		const [name, property] = group[0];
		const custom = group[1];
		if (!answerableProperty(property)) return defer;
		pages.push({
			question: property.description?.trim() || property.title?.trim() || humanizeInputName(name),
			options: inputOptions(property).map((option) => ({ id: option.value, label: option.label, description: option.description })),
			freeText: custom ? freeTextFor(custom[0], custom[1]) : freeTextFor(name, property),
			propertyName: name,
			multi: property.type === "array",
			required: required.includes(name),
		});
	}

	// A page with neither options nor a typed answer asks nothing.
	if (pages.some((page) => !page.options.length && !page.freeText)) return defer;

	return { ...base, pages, canAnswerInline: true };
}

/**
 * The card renders a list of choices and one text row. A boolean is a switch and
 * an array of objects is a form — neither survives that shape.
 */
function answerableProperty(property: InputProperty): boolean {
	if (property.type === "boolean") return false;
	if (property.type === "array" && !inputOptions(property).length) return false;
	return true;
}

/** A typed answer is offered for free-text properties, never for a closed enum. */
function freeTextFor(name: string, property: InputProperty): RequestDockFreeText | undefined {
	if (inputOptions(property).length) return undefined;
	if (property.type === "boolean" || property.type === "array") return undefined;
	return {
		name,
		placeholder: property.title?.trim() || "Type your answer…",
		numeric: property.type === "number" || property.type === "integer",
		maxLength: property.maxLength,
	};
}
