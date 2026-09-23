import { describe, expect, it } from "vitest";

import { requestDockModel } from "./requestDockModel";
import type { ConversationActivity, ConversationSnapshot, InputProperty } from "./types";

const activity = (over: Partial<ConversationActivity> = {}): ConversationActivity =>
	({
		kind: "activity",
		activityKind: "approval",
		status: "pending",
		sequence: 7,
		requestId: "req-1",
		summary: "",
		...over,
	}) as ConversationActivity;

const snapshot = (items: ConversationActivity[]): ConversationSnapshot =>
	({ items, turns: [] }) as unknown as ConversationSnapshot;

const idle = { approval: false, input: false };

const elicitation = (properties: Record<string, InputProperty>, over: Partial<ConversationActivity> = {}) =>
	activity({
		activityKind: "user_input",
		detail: { message: "Answer these", schema: { properties } },
		...over,
	} as Partial<ConversationActivity>);

describe("requestDockModel", () => {
	it("shows nothing when nothing is pending", () => {
		expect(requestDockModel(snapshot([]), idle)).toBeNull();
		expect(requestDockModel(undefined, idle)).toBeNull();
	});

	describe("approvals", () => {
		it("asks the reason and numbers the decisions", () => {
			const model = requestDockModel(
				snapshot([activity({
					detail: { reason: "Run the test suite?", command: "npm test" } as ConversationActivity["detail"],
					decisions: [{ id: "allow", label: "Allow" }, { id: "deny", label: "Deny" }],
				})]),
				idle,
			);
			expect(model?.kind).toBe("approval");
			expect(model?.title).toBe("Approval needed");
			expect(model?.pages).toHaveLength(1);
			expect(model?.pages[0].question).toBe("Run the test suite?");
			expect(model?.pages[0].options.map((o) => o.id)).toEqual(["allow", "deny"]);
			expect(model?.canAnswerInline).toBe(true);
		});

		// The command is what will actually run, so it is the honest fallback.
		it("falls back to the command when there is no reason", () => {
			const model = requestDockModel(
				snapshot([activity({ detail: { command: "rm -rf build" } as ConversationActivity["detail"], decisions: [{ id: "a", label: "Allow" }] })]),
				idle,
			);
			expect(model?.pages[0].question).toBe("rm -rf build");
		});

		it("never offers a typed answer", () => {
			const model = requestDockModel(snapshot([activity({ decisions: [{ id: "a", label: "Allow" }] })]), idle);
			expect(model?.pages[0].freeText).toBeUndefined();
		});

		it("defers when the agent offered no decisions", () => {
			expect(requestDockModel(snapshot([activity({ decisions: [] })]), idle)?.canAnswerInline).toBe(false);
		});
	});

	describe("elicitations", () => {
		const twoQuestions = {
			question_1: { type: "string", description: "Which branch?", enum: ["main", "develop"] },
			question_2: { type: "string", description: "How much cooking are you up for?", enum: ["Barely any", "Some is fine"] },
		} as Record<string, InputProperty>;

		it("pages one question at a time, in order", () => {
			const model = requestDockModel(snapshot([elicitation(twoQuestions)]), idle);
			expect(model?.kind).toBe("input");
			expect(model?.title).toBe("Agent needs input");
			expect(model?.pages).toHaveLength(2);
			expect(model?.pages[0].question).toBe("Which branch?");
			expect(model?.pages[1].question).toBe("How much cooking are you up for?");
			expect(model?.pages[1].options.map((o) => o.label)).toEqual(["Barely any", "Some is fine"]);
			expect(model?.canAnswerInline).toBe(true);
		});

		// question_N_custom is the schema's own way of saying "or write your own".
		it("turns a _custom sibling into the typed-answer row", () => {
			const model = requestDockModel(snapshot([elicitation({
				question_1: { type: "string", description: "Pick one", enum: ["a", "b"] },
				question_1_custom: { type: "string", title: "Something else" },
			} as Record<string, InputProperty>)]), idle);
			expect(model?.pages).toHaveLength(1);
			expect(model?.pages[0].options).toHaveLength(2);
			expect(model?.pages[0].freeText?.name).toBe("question_1_custom");
			expect(model?.pages[0].freeText?.placeholder).toBe("Something else");
		});

		it("offers only a typed answer when the question has no choices", () => {
			const model = requestDockModel(snapshot([elicitation({
				question_1: { type: "string", description: "What should I name it?" },
			} as Record<string, InputProperty>)]), idle);
			expect(model?.pages[0].options).toEqual([]);
			expect(model?.pages[0].freeText?.name).toBe("question_1");
			expect(model?.pages[0].freeText?.numeric).toBe(false);
			expect(model?.canAnswerInline).toBe(true);
		});

		it("marks a numeric answer so the right keyboard opens", () => {
			const model = requestDockModel(snapshot([elicitation({
				question_1: { type: "integer", description: "How many retries?" },
			} as Record<string, InputProperty>)]), idle);
			expect(model?.pages[0].freeText?.numeric).toBe(true);
		});

		it("carries required so an optional question can be skipped", () => {
			const model = requestDockModel(snapshot([elicitation(
				{ question_1: { type: "string", description: "Which branch?", enum: ["main"] } } as Record<string, InputProperty>,
				{ detail: { message: "m", schema: { properties: { question_1: { type: "string", description: "Which branch?", enum: ["main"] } }, required: ["question_1"] } } } as Partial<ConversationActivity>,
			)]), idle);
			expect(model?.pages[0].required).toBe(true);
		});
	});

	// The card renders choices and one text row. Anything richer belongs in the
	// inline form, which owns the fields and the validation.
	describe("deferring to the inline form", () => {
		const defers = (properties: Record<string, InputProperty>) =>
			requestDockModel(snapshot([elicitation(properties)]), idle)?.canAnswerInline;

		it("defers on a boolean", () => {
			expect(defers({ question_1: { type: "boolean", description: "Proceed?" } } as Record<string, InputProperty>)).toBe(false);
		});

		it("defers when the property names are not the question convention", () => {
			expect(defers({ branch: { type: "string", description: "Which branch?" }, message: { type: "string" } } as Record<string, InputProperty>)).toBe(false);
		});

		it("defers on a URL handshake", () => {
			const model = requestDockModel(snapshot([activity({
				activityKind: "user_input",
				detail: { inputMode: "url", url: "https://example.com", message: "Sign in" },
			} as Partial<ConversationActivity>)]), idle);
			expect(model?.canAnswerInline).toBe(false);
			expect(model?.pages[0].question).toBe("Sign in");
		});

		it("defers when there is no request id to answer with", () => {
			const model = requestDockModel(snapshot([elicitation(
				{ question_1: { type: "string", description: "Which?", enum: ["a"] } } as Record<string, InputProperty>,
				{ requestId: undefined },
			)]), idle);
			expect(model?.canAnswerInline).toBe(false);
		});

		it("still reports the sequence, so the card can be reached", () => {
			const model = requestDockModel(snapshot([elicitation(
				{ nope: { type: "boolean" } } as Record<string, InputProperty>,
				{ sequence: 91 },
			)]), idle);
			expect(model?.sequence).toBe(91);
		});
	});

	it("prefers a pending approval over a pending input", () => {
		const model = requestDockModel(
			snapshot([elicitation({ question_1: { type: "string", enum: ["a"] } } as Record<string, InputProperty>, { sequence: 1 }), activity({ sequence: 2, decisions: [{ id: "a", label: "Allow" }] })]),
			idle,
		);
		expect(model?.kind).toBe("approval");
		expect(model?.sequence).toBe(2);
	});

	it("passes the matching in-flight flag through as busy", () => {
		expect(requestDockModel(snapshot([activity({ decisions: [{ id: "a", label: "A" }] })]), { approval: true, input: false })?.busy).toBe(true);
	});
});
