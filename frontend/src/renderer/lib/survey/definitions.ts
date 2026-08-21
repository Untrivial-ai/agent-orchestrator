// The in-product survey: five short questions the user answers from the
// sidebar "Help shape AO" invite. Each is answerable at a glance; together they
// cover who the user is, how they work, whether it sticks, what slows them
// down, and one open wish. Answers are captured to telemetry (see
// surveyController.ts) so the responses become user metrics.

/** single = one tap (auto-advances); multi = pick many + Continue; text = short answer. */
export type SurveyInput = "single" | "multi" | "text";

export type Question = {
	/** Stable id; also the `answer_<id>` property on the completed event. */
	id: string;
	input: SurveyInput;
	question: string;
	/** Options for single/multi. */
	choices?: string[];
	/** Placeholder for text. */
	placeholder?: string;
};

/** The ordered survey. Reorder or edit here to change what users see. */
export const SURVEY_FORM: Question[] = [
	{
		id: "profile",
		input: "single",
		question: "What best describes you?",
		choices: ["Developer", "Founder", "Manager", "Freelancer", "Student", "Other"],
	},
	{
		id: "task-type",
		input: "multi",
		question: "What did you use this AO for?",
		choices: ["Bug fix", "New feature", "Refactor", "Tests", "CI / maintenance", "Exploring"],
	},
	{
		id: "pmf",
		input: "single",
		question: "If AO disappeared tomorrow?",
		choices: ["I'd be lost", "Mildly annoyed", "No big deal"],
	},
	{
		id: "blocker",
		input: "multi",
		question: "What slows you down most in AO?",
		choices: ["Setup", "Speed", "Output quality", "Managing many agents", "Nothing"],
	},
	{
		id: "wish",
		input: "text",
		question: "One thing you wish AO did automatically?",
		placeholder: "e.g. auto-open a PR when CI is green",
	},
];
