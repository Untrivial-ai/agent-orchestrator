import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";

import { SURVEY_FORM } from "../lib/survey/definitions";
import { closeSurvey, completeSurvey, isSurveyOpen, recordAnswer, subscribeSurvey } from "../lib/survey";

// Scoped styles, injected once. Uses AO's own theme tokens so the modal tracks
// light/dark with the app.
const STYLE_ID = "ao-survey-styles";
const CSS = `
@keyframes ao-survey-pop{from{opacity:0;transform:translateY(8px) scale(.97)}to{opacity:1;transform:none}}
.aoq-scrim{position:fixed;inset:0;z-index:60;display:flex;align-items:center;justify-content:center;padding:24px;
  background:rgba(10,13,20,.55);backdrop-filter:blur(2px)}
.aoq-modal{width:100%;max-width:480px;background:var(--color-popover,#fff);color:var(--color-popover-foreground,#131722);
  border:1px solid var(--color-border,#e7eaef);border-radius:20px;overflow:hidden;
  box-shadow:0 24px 70px -18px rgba(15,23,42,.5);animation:ao-survey-pop .24s cubic-bezier(.2,.7,.2,1);font-size:14px}
.aoq-head{padding:16px 20px 0}
.aoq-top{display:flex;align-items:center;gap:10px}
.aoq-eye{font-size:11px;font-weight:700;letter-spacing:.04em;text-transform:uppercase;color:var(--color-accent,#2f5ff0)}
.aoq-count{margin-left:auto;font-size:12px;color:var(--color-muted-foreground,#69727f);font-variant-numeric:tabular-nums}
.aoq-x{width:26px;height:26px;border:none;background:none;color:var(--color-muted-foreground,#9aa2ad);cursor:pointer;border-radius:7px;font-size:17px;line-height:1}
.aoq-x:hover{color:var(--color-popover-foreground,#131722);background:var(--color-accent-weak,#f2f4f7)}
.aoq-track{height:4px;border-radius:99px;background:var(--color-accent-weak,#f2f4f7);margin-top:12px;overflow:hidden}
.aoq-fill{height:100%;background:var(--color-accent,#2f5ff0);border-radius:99px;transition:width .3s cubic-bezier(.2,.7,.2,1)}
.aoq-body{padding:20px 20px 6px}
.aoq-q{margin:0 0 4px;font-size:19px;font-weight:650;letter-spacing:-.01em;line-height:1.3}
.aoq-sub{margin:0 0 16px;color:var(--color-muted-foreground,#69727f);font-size:13px}
.aoq-opts{display:flex;flex-direction:column;gap:9px}
.aoq-opt{position:relative;display:flex;align-items:center;gap:11px;text-align:left;cursor:pointer;font:inherit;font-size:14.5px;
  color:inherit;border:1.5px solid var(--color-border,#e7eaef);background:transparent;border-radius:13px;padding:13px 14px;transition:.13s}
.aoq-opt:hover{border-color:var(--color-accent,#2f5ff0);background:var(--color-accent-weak,#eef3ff)}
.aoq-opt.sel{border-color:var(--color-accent,#2f5ff0);background:var(--color-accent-weak,#eef3ff)}
.aoq-mark{width:20px;height:20px;border-radius:50%;border:2px solid var(--color-border,#cfd6df);flex:none}
.aoq-opt.multi .aoq-mark{border-radius:7px}
.aoq-opt.sel .aoq-mark{border-color:var(--color-accent,#2f5ff0);background:var(--color-accent,#2f5ff0)}
.aoq-opt.sel .aoq-mark::after{content:"✓";color:var(--color-accent-foreground,#fff);font-size:12px;display:flex;align-items:center;justify-content:center;height:100%}
.aoq-ta{width:100%;min-height:110px;resize:none;font:inherit;font-size:14.5px;color:inherit;background:var(--color-accent-weak,#f2f4f7);
  border:1.5px solid var(--color-border,#e7eaef);border-radius:13px;padding:12px 13px}
.aoq-ta:focus{outline:none;border-color:var(--color-accent,#2f5ff0)}
.aoq-foot{display:flex;align-items:center;gap:10px;padding:16px 20px 18px}
.aoq-ghost{font:inherit;font-size:13.5px;font-weight:600;color:var(--color-muted-foreground,#69727f);background:none;border:none;cursor:pointer;padding:8px 6px}
.aoq-ghost:hover{color:var(--color-popover-foreground,#131722)}
.aoq-spacer{flex:1}
.aoq-cta{font:inherit;font-size:14px;font-weight:650;cursor:pointer;background:var(--color-accent,#2f5ff0);color:var(--color-accent-foreground,#fff);border:none;border-radius:12px;padding:10px 20px}
.aoq-cta:disabled{opacity:.45;cursor:not-allowed}
.aoq-done{padding:40px 24px;text-align:center}
.aoq-ring{width:52px;height:52px;border-radius:50%;background:var(--color-accent-weak,#eef3ff);color:var(--color-accent,#2f5ff0);display:flex;align-items:center;justify-content:center;font-size:24px;margin:0 auto 14px}
.aoq-done h3{margin:0 0 4px;font-size:19px}.aoq-done p{margin:0;color:var(--color-muted-foreground,#69727f);font-size:14px}
`;
function ensureStyles() {
	if (typeof document === "undefined" || document.getElementById(STYLE_ID)) return;
	const el = document.createElement("style");
	el.id = STYLE_ID;
	el.textContent = CSS;
	document.head.appendChild(el);
}

/**
 * A centered, stepped survey modal. Opens over a dimmed backdrop when a survey
 * is triggered, walks the question pool one at a time with a progress bar, and
 * records each answer. Single-select auto-advances; multi and text use
 * Continue. Guarded to appear rarely by the controller's weekly cap.
 */
export function SurveyPrompt() {
	const { t } = useTranslation();
	const active = useSyncExternalStore(subscribeSurvey, isSurveyOpen, isSurveyOpen);
	const [step, setStep] = useState(0);
	const [multi, setMulti] = useState<string[]>([]);
	const [text, setText] = useState("");
	// Answers captured so far this session, so navigating Back restores what the
	// user picked instead of re-showing the question blank.
	const responses = useRef<Record<string, string[] | string>>({});
	// Pending single-select auto-advance, held so we can cancel it on close and
	// ignore a second click before it fires.
	const advanceTimer = useRef<number | null>(null);

	const clearAdvance = () => {
		if (advanceTimer.current !== null) {
			window.clearTimeout(advanceTimer.current);
			advanceTimer.current = null;
		}
	};

	// Fresh start every time the survey opens, so a prior session's step or a
	// stray timer can never carry over.
	useEffect(() => {
		if (!active) return;
		responses.current = {};
		setStep(0);
		setMulti([]);
		setText("");
	}, [active]);

	// Restore the stored answer for whichever question is now showing (Back/next).
	useEffect(() => {
		const q = SURVEY_FORM[step];
		if (!q) return;
		const prev = responses.current[q.id];
		if (q.input === "multi") setMulti(Array.isArray(prev) ? prev : []);
		else if (q.input === "text") setText(typeof prev === "string" ? prev : "");
	}, [step]);

	// Cancel any pending auto-advance if the modal unmounts.
	useEffect(
		() => () => {
			if (advanceTimer.current !== null) window.clearTimeout(advanceTimer.current);
		},
		[],
	);

	ensureStyles();
	if (!active) return null;

	const total = SURVEY_FORM.length;
	const finished = step >= total;
	const survey = SURVEY_FORM[step];

	const record = (id: string, value: string | string[]) => {
		responses.current[id] = value;
		recordAnswer(id, value);
	};
	const advance = () => {
		clearAdvance();
		setStep((s) => s + 1);
	};
	// Single-select auto-advances after a short beat; guard against a double click
	// firing two advances and skipping the next question.
	const selectSingle = (id: string, choice: string) => {
		if (advanceTimer.current !== null) return;
		record(id, choice);
		advanceTimer.current = window.setTimeout(() => {
			advanceTimer.current = null;
			setStep((s) => s + 1);
		}, 220);
	};
	// Closing mid-survey (the ×) just dismisses; finishing marks it complete so
	// the invite never returns.
	const close = () => {
		clearAdvance();
		setStep(0);
		closeSurvey();
	};
	const complete = () => {
		clearAdvance();
		setStep(0);
		completeSurvey();
	};

	if (finished) {
		return (
			<div className="aoq-scrim">
				<div className="aoq-modal">
					<div className="aoq-done">
						<div className="aoq-ring">✓</div>
						<h3>{t("survey.thanksTitle")}</h3>
						<p>{t("survey.thanksBody")}</p>
					</div>
					<div className="aoq-foot">
						<span className="aoq-spacer" />
						<button type="button" className="aoq-cta" onClick={complete}>
							{t("survey.done")}
						</button>
					</div>
				</div>
			</div>
		);
	}

	const isMulti = survey.input === "multi";
	const isText = survey.input === "text";
	const canContinue = isText ? true : multi.length > 0;

	const toggle = (choice: string) =>
		setMulti((p) => (p.includes(choice) ? p.filter((c) => c !== choice) : [...p, choice]));

	return (
		<div className="aoq-scrim" role="dialog" aria-label={t("survey.dialogAria")}>
			<div className="aoq-modal">
				<div className="aoq-head">
					<div className="aoq-top">
						<span className="aoq-eye">{t("survey.eyebrow")}</span>
						<span className="aoq-count">{t("survey.progress", { current: step + 1, total })}</span>
						<button type="button" className="aoq-x" aria-label={t("common.close")} onClick={close}>
							×
						</button>
					</div>
					<div className="aoq-track">
						<div className="aoq-fill" style={{ width: `${Math.round((step / total) * 100)}%` }} />
					</div>
				</div>

				<div className="aoq-body">
					<p className="aoq-q">{survey.question}</p>
					{isText ? (
						<textarea
							className="aoq-ta"
							placeholder={survey.placeholder}
							value={text}
							onChange={(e) => setText(e.target.value)}
						/>
					) : (
						<div className="aoq-opts">
							{(survey.choices ?? []).map((choice) => {
								const sel = isMulti ? multi.includes(choice) : false;
								return (
									<button
										key={choice}
										type="button"
										className={`aoq-opt${isMulti ? " multi" : ""}${sel ? " sel" : ""}`}
										onClick={() => {
											if (isMulti) {
												toggle(choice);
											} else {
												selectSingle(survey.id, choice);
											}
										}}
									>
										<span className="aoq-mark" />
										{choice}
									</button>
								);
							})}
						</div>
					)}
				</div>

				<div className="aoq-foot">
					{step > 0 ? (
						<button type="button" className="aoq-ghost" onClick={() => setStep((s) => s - 1)}>
							{t("survey.back")}
						</button>
					) : null}
					<span className="aoq-spacer" />
					{isText ? (
						<button type="button" className="aoq-ghost" onClick={advance}>
							{t("survey.skip")}
						</button>
					) : null}
					{isMulti || isText ? (
						<button
							type="button"
							className="aoq-cta"
							disabled={!canContinue}
							onClick={() => {
								record(survey.id, isText ? text.trim() || "(empty)" : multi);
								advance();
							}}
						>
							{step === total - 1 ? t("survey.finish") : t("survey.continue")}
						</button>
					) : null}
				</div>
			</div>
		</div>
	);
}
