import { useState, type ComponentProps, type KeyboardEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { AlertCircle, Check, ChevronDown, ChevronRight, CirclePause, Copy, ExternalLink, Laptop, Link2, LoaderCircle, X, type LucideIcon } from "lucide-react";
import { PROVIDERS, percentLeft, type ProviderAccount, type ProviderLogin } from "../../hooks/useProviderAccounts";
import { useSessionUsageSummaries } from "../../hooks/useSessionUsageSummaries";
import { useWorkspaceQuery } from "../../hooks/useWorkspaceQuery";
import { aoBridge } from "../../lib/bridge";
import { findSession } from "../../lib/command-palette";
import { formatCostNanos } from "../../lib/format-cost";
import { useNavigateToSession } from "../../lib/navigate-to-session";
import { cn } from "../../lib/utils";
import { useUiStore } from "../../stores/ui-store";
import { AccountMenu, AccountMenuItems } from "../AccountMenu";
import { AgentAvatar } from "../AgentAvatar";
import { Button } from "../ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "../ui/dropdown-menu";
import { Switch } from "../ui/switch";
import type { AccountsPage } from "./ProviderAccountsSection";

const DAY = 24 * 60 * 60;
// Where each provider shows the plan, its usage and its billing.
const PLAN_PAGES = { claude: "https://claude.ai/settings/usage", codex: "https://chatgpt.com/codex/settings/usage" } as const;
const SCOPES = { code_review: "providerAccounts.limitCodeReview", cowork: "providerAccounts.limitCowork", oauth_apps: "providerAccounts.limitApps" } as const;
const WAITING = { browser: "providerAccounts.browserSignIn", device: "providerAccounts.deviceSignIn", import: "providerAccounts.importWaiting", api_key: "providerAccounts.apiKeyWaiting" } as const;
const RESET_OUTCOMES = { reset: "providerAccounts.resetDone", nothing_to_reset: "providerAccounts.resetNothing", none_available: "providerAccounts.resetNone", wait: "providerAccounts.resetWait", failed: "providerAccounts.resetFailed", unknown: "providerAccounts.resetUnknown" } as const;
const FAILURES = { limit: "providerAccounts.limitReached", "sign-in": "providerAccounts.failureSignIn", server: "providerAccounts.failureServer", other: "providerAccounts.failureOther" } as const;
const SHOWN = 5;
const danger = "bg-destructive/15 text-destructive hover:bg-destructive/25 dark:hover:bg-destructive/25";
// A day (with its year when that is not this year), a day and time, or for "soon" the time alone today.
function formatWhen(value: string, style: "day" | "time" | "soon"): string {
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) return value;
	const now = new Date();
	const clock = { hour: "numeric", minute: "2-digit" } as const;
	const options: Intl.DateTimeFormatOptions = style === "day"
		? { month: "short", day: "numeric", year: date.getFullYear() === now.getFullYear() ? undefined : "numeric" }
		: style === "soon" && date.toDateString() === now.toDateString() ? clock : { month: "short", day: "numeric", ...clock };
	return new Intl.DateTimeFormat(undefined, options).format(date);
}
function formatAgo(time: number, language: string, justNow: string): string {
	const minutes = Math.floor((Date.now() - time) / 60_000);
	if (!(minutes >= 1)) return justNow;
	const [amount, unit] = minutes < 60 ? [minutes, "minute"] as const : minutes < 1440 ? [Math.floor(minutes / 60), "hour"] as const : [Math.floor(minutes / 1440), "day"] as const;
	return new Intl.RelativeTimeFormat(language, { numeric: "always" }).format(-amount, unit);
}
const formatCount = (value: number, language: string) => new Intl.NumberFormat(language, { notation: "compact", maximumFractionDigits: 1 }).format(value);
const formatSpan = (value: number, unit: "day" | "hour" | "minute", language: string) => new Intl.NumberFormat(language, { style: "unit", unit, unitDisplay: unit === "day" ? "long" : "short" }).format(value);
function formatTurn(seconds: number, language: string): string {
	const minutes = Math.max(1, Math.round(seconds / 60));
	const hours = minutes >= 60 ? formatSpan(Math.floor(minutes / 60), "hour", language) : "";
	return [hours, minutes % 60 ? formatSpan(minutes % 60, "minute", language) : ""].filter(Boolean).join(" ");
}
// The provider counts tokens by calendar day; "today" is its day or ours ("sv" writes a date as YYYY-MM-DD).
const isToday = (day: string) => [new Date().toISOString().slice(0, 10), new Date().toLocaleDateString("sv")].includes(day);
// A time that has not passed yet, or "".
export const upcoming = (value?: string) => (value && new Date(value).getTime() > Date.now() ? value : "");
// This computer's own login or key, marked beside the account's name wherever it is listed.
export function DeviceTag({ apiKey, testId }: { apiKey: boolean; testId?: string }) {
	const { t } = useTranslation();
	return (
		<span data-testid={testId} title={t(apiKey ? "providerAccounts.globalKeyHint" : "providerAccounts.globalHint")} className="inline-flex shrink-0 items-center gap-1 rounded-full bg-status-working/15 px-2 py-0.5 text-2xs font-medium text-status-working">
			<Laptop aria-hidden="true" className="size-3" />{t("providerAccounts.global")}
		</span>
	);
}
export const Dot = () => <span aria-hidden="true" className="text-passive">·</span>;
// A group of settings rows under its title: one softly filled surface with hairlines between its rows.
export function Rows({ title, className, ...props }: Omit<ComponentProps<"div">, "title"> & { title?: string }) {
	return (
		<>
			{title ? <h4 className="mb-2 mt-6 text-sm font-normal text-muted-foreground">{title}</h4> : null}
			<div className={cn("divide-y divide-border rounded-xl bg-foreground/[0.035]", className)} {...props} />
		</>
	);
}
export function IconAction({ name, icon: Icon, className, children, ...props }: ComponentProps<typeof Button> & { name: string; icon?: LucideIcon }) {
	return <Button type="button" size="icon-sm" variant="ghost" className={cn("text-muted-foreground", className)} aria-label={name} title={name} {...props}>{Icon ? <Icon aria-hidden="true" className="size-3.5" /> : children}</Button>;
}
function Row({ title, hint, children, label, icon }: { title: ReactNode; hint?: ReactNode; children?: ReactNode; label?: string; icon?: ReactNode }) {
	return (
		<div role={label ? "group" : undefined} aria-label={label} className="flex min-h-15 flex-wrap items-center justify-between gap-x-5 gap-y-2 px-4 py-3">
			<div className="flex min-w-0 items-center gap-3">
				{icon}
				<div className="min-w-0">
					<p className="text-sm font-medium text-foreground">{title}</p>
					{hint ? <p className="mt-px text-xs text-muted-foreground">{hint}</p> : null}
				</div>
			</div>
			{children ? <div className="flex shrink-0 items-center gap-2">{children}</div> : null}
		</div>
	);
}
function Fact({ label, children }: { label: ReactNode; children: ReactNode }) {
	return (
		<div className="flex min-h-11 items-center justify-between gap-4 px-4 py-2 text-[13px]">
			<span className="min-w-0 truncate text-muted-foreground">{label}</span>
			<span className="flex shrink-0 items-center gap-1.5 tabular-nums text-foreground">{children}</span>
		</div>
	);
}
// A button that shows what is chosen and opens the menu of choices.
function Choice({ label, menuClassName = "min-w-52", children, ...props }: ComponentProps<typeof Button> & { label: string; menuClassName?: string }) {
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button type="button" variant="secondary" className="max-w-56 gap-1.5 font-normal" {...props}><span className="min-w-0 truncate">{label}</span><ChevronDown aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" /></Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end" className={cn("text-xs", menuClassName)}>{children}</DropdownMenuContent>
		</DropdownMenu>
	);
}
const Tick = ({ on }: { on: boolean }) => <Check aria-hidden="true" className={on ? "size-3.5" : "invisible size-3.5"} />;
// What is left of an allowance. Colour appears only when it is nearly used (caution) or used up (needs attention).
function MeterRow({ testId, title, hint, percent, value, meterLabel = title }: { testId?: string; title: string; hint?: string; percent: number; value?: string; meterLabel?: string }) {
	const { t } = useTranslation();
	const reached = percent === 0;
	const low = !reached && percent <= 20;
	return (
		<div data-testid={testId} className="flex min-h-15 items-center gap-5 px-4 py-3">
			<div className="w-44 shrink-0">
				<p className="truncate text-sm font-medium text-foreground" title={title}>{title}</p>
				{hint ? <p className="mt-px truncate text-xs text-muted-foreground">{hint}</p> : null}
			</div>
			<div role="progressbar" aria-label={meterLabel} aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} className={cn("h-1.5 min-w-0 max-w-[420px] flex-1 overflow-hidden rounded-full", reached ? "bg-status-needs-you/40" : "bg-muted")}>
				<div className={cn("h-full rounded-full transition-[width]", low ? "bg-warning" : "bg-muted-foreground")} style={{ width: `${percent}%` }} />
			</div>
			<span className={cn("ml-auto min-w-20 shrink-0 text-right text-sm tabular-nums", reached ? "text-status-needs-you" : low ? "text-warning" : "text-foreground")}>
				{value ?? (reached ? t("providerAccounts.usageReached") : t("providerAccounts.usageRemaining", { percent }))}
			</span>
		</div>
	);
}
function CopyIcon({ value, label, icon: Icon, page }: { value: string; label: string; icon: LucideIcon; page: AccountsPage }) {
	const { t } = useTranslation();
	const [copied, setCopied] = useState(false);
	function showTick() {
		setCopied(true);
		setTimeout(() => setCopied(false), 1500);
	}
	return (
		<IconAction name={copied ? t("startup.commandCopied") : label} onClick={() => void aoBridge.clipboard.writeText(value).then(showTick, (error: Error) => page.say(error.message))}>
			{copied ? <Check aria-hidden="true" className="size-3.5 text-status-ready" /> : <Icon aria-hidden="true" className="size-3.5" />}
		</IconAction>
	);
}
// A sign-in in progress: one line, with small icon actions at its end.
export function LoginProgress({ login, page }: { login: ProviderLogin; page: AccountsPage }) {
	const { t } = useTranslation();
	return (
		<>
			<div className="flex min-h-7 items-center gap-2.5 text-sm text-foreground">
				<LoaderCircle aria-hidden="true" className="size-3.5 shrink-0 animate-spin text-status-working" />
				<span className="min-w-0 flex-1">{t(WAITING[login.mode], { provider: PROVIDERS.find((provider) => provider.id === login.provider)?.name })}</span>
				<span className="flex shrink-0 items-center gap-0.5">
					{login.url ? <IconAction name={t("providerAccounts.openSignInPage")} icon={ExternalLink} onClick={() => void page.run(() => aoBridge.app.openExternal(login.url!))} /> : null}
					{login.url ? <CopyIcon value={login.url} label={t("link.copy")} icon={Link2} page={page} /> : null}
					<IconAction name={t("providerAccounts.cancelSignIn")} icon={X} disabled={page.pending} onClick={() => void page.run(page.signIn.cancel)} />
				</span>
			</div>
			{login.mode === "device" && login.code ? (
				<div className="mt-2 inline-flex h-8 items-center gap-2 rounded-md border border-input bg-background pl-3 pr-0.5 font-mono text-sm font-medium tracking-widest text-foreground">
					{login.code}
					<CopyIcon value={login.code} label={t("providerAccounts.copyCode")} icon={Copy} page={page} />
				</div>
			) : null}
		</>
	);
}
// The sessions on the account by name, busiest today first, each one a click from being opened or moved.
function AccountSessions({ account, page, others, language }: { account: ProviderAccount; page: AccountsPage; others: ProviderAccount[]; language: string }) {
	const { t } = useTranslation();
	const openSession = useNavigateToSession();
	const closeSettings = useUiStore((state) => state.closeSettings);
	const [all, setAll] = useState(false);
	const workspaces = useWorkspaceQuery({ includeCloud: false }).data ?? [];
	const today = account.usage?.activity?.sessions ?? {};
	const rows = account.sessions.flatMap((id) => findSession(workspaces, id) ?? []).sort((first, second) => (today[second.session.id] ?? 0) - (today[first.session.id] ?? 0));
	if (!rows.length) return null;
	return (
		<Rows title={t("providerAccounts.sessionsOnAccount", { count: rows.length })} data-testid="provider-account-sessions">
			{(all ? rows : rows.slice(0, SHOWN)).map(({ workspace, session }) => (
				<div key={session.id} className="flex min-h-11 items-center gap-3 py-1.5 pl-4 pr-2 text-[13px]">
					<span aria-hidden="true" className={cn("size-1.5 shrink-0 rounded-full", session.status === "working" ? "bg-status-working" : "bg-muted-foreground/50")} />
					<span className="min-w-0 flex-1 truncate text-foreground" title={session.title}>{session.title || session.id}</span>
					{today[session.id] ? <span className="shrink-0 tabular-nums text-muted-foreground">{t("providerAccounts.tokensTodayShort", { tokens: formatCount(today[session.id], language) })}</span> : null}
					{others.length ? (
						<AccountMenu accounts={others} onSelect={(target) => void page.act(target.id, { action: "assign-session", sessionId: session.id }, t("providerAccounts.sessionMoved", { name: target.displayName }))}>
							<IconAction name={t("providerAccounts.moveSession")} icon={ChevronDown} disabled={page.pending} />
						</AccountMenu>
					) : null}
					<IconAction name={t("automations.runs.openSession")} icon={ChevronRight} onClick={() => {
						closeSettings();
						openSession(workspace.id, session.id);
					}} />
				</div>
			))}
			{rows.length > SHOWN && !all ? (
				<button type="button" className="flex min-h-10 w-full items-center px-4 text-left text-[13px] text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring" onClick={() => setAll(true)}>{t("providerAccounts.showMoreSessions", { count: rows.length - SHOWN })}</button>
			) : null}
		</Rows>
	);
}
// What passed through AO on this account: a bar a day, then the totals.
function AccountThroughput({ account, language }: { account: ProviderAccount; language: string }) {
	const { t } = useTranslation();
	const activity = account.usage?.activity;
	const costs = useSessionUsageSummaries().data;
	const nanos = account.sessions.reduce((sum, id) => sum + (costs?.get(id)?.estimatedCost?.totalNanos ?? 0), 0);
	const days = activity?.days ?? [];
	const peak = Math.max(1, ...days.map((day) => day.tokens));
	const day = (date: string) => new Intl.DateTimeFormat(language, { month: "short", day: "numeric" }).format(new Date(`${date}T12:00:00`));
	return (
		<>
			{days.some((entry) => entry.tokens) ? (
				<div className="px-4 pb-2.5 pt-3.5" role="img" aria-label={t("providerAccounts.tokensPerDay")}>
					<div className="flex h-14 items-end gap-1">
						{days.map((entry, index) => <span key={entry.date} title={`${day(entry.date)} · ${formatCount(entry.tokens, language)}`} className={cn("min-h-0.5 flex-1 rounded-sm", index === days.length - 1 ? "bg-foreground" : "bg-muted-foreground/55")} style={{ height: `${(entry.tokens / peak) * 100}%` }} />)}
					</div>
					<div className="mt-1.5 flex justify-between text-2xs text-passive"><span>{day(days[0]!.date)}</span><span>{t("providerAccounts.tokensPerDay")}</span><span>{t("providerAccounts.today")}</span></div>
				</div>
			) : null}
			{activity ? (
				<>
					<Fact label={t("providerAccounts.throughToday")}>{formatCount(activity.today, language)}</Fact>
					<Fact label={t("providerAccounts.throughWeek")}>{formatCount(activity.week, language)}</Fact>
					{activity.since ? <Fact label={t("providerAccounts.throughSince", { date: day(activity.since) })}>{formatCount(activity.total, language)}</Fact> : null}
				</>
			) : null}
			{nanos > 0 ? <Fact label={<span title={t("providerAccounts.costHint")}>{t("providerAccounts.sessionsCost")}</span>}>{formatCostNanos(nanos)}</Fact> : null}
		</>
	);
}
// The selected account: each action is a row with one control, beside (when signed in) a column that is only read.
export function AccountDetail({ account, page }: { account: ProviderAccount; page: AccountsPage }) {
	const { t, i18n: { language } } = useTranslation();
	const [removing, setRemoving] = useState(false);
	const [resetting, setResetting] = useState(false);
	const [picked, setPicked] = useState("");
	const info = PROVIDERS.find((provider) => provider.id === account.provider)!;
	const { signedIn, displayName: name } = account;
	const apiKey = account.kind === "api_key";
	const count = account.sessions.length;
	// An API key has no limits to report, but the helper still knows its activity.
	const usage = account.usage?.status === "available" || apiKey ? account.usage : undefined;
	const known = account.usage; // The plan is shown whenever it is known, even while the limits are not.
	const plan = known?.plan ? [known.plan.charAt(0).toUpperCase() + known.plan.slice(1), known.planTier].filter(Boolean).join(" ") : "";
	const others = page.accounts.filter((other) => other.provider === account.provider && other.signedIn && other.id !== account.id);
	const currentDefault = page.accounts.find((other) => other.provider === account.provider && other.primary);
	const offerFrom = page.moveOffer?.toId === account.id && account.primary ? page.accounts.find((other) => other.id === page.moveOffer?.fromId) : undefined;
	const signingIn = page.signIn.waiting && page.signIn.login?.accountId === account.id ? page.signIn.login : null;
	// Removing the default needs a new one; the next signed-in account is offered straight away.
	const replacement = account.primary ? others.find((other) => other.id === picked) ?? others[0] : undefined;
	const fallback = others.find((other) => other.primary);
	const removalFact = replacement ? t("providerAccounts.chooseNewDefault")
		: !count ? undefined
			: !others.length ? t("providerAccounts.sessionsWaitForSignIn", { count })
				: fallback ? t("providerAccounts.sessionsMoveTo", { count, name: fallback.displayName }) : undefined;
	// The default only decides where a new session starts, so it changes at once; running sessions stay unless moved.
	async function makeDefault() {
		setRemoving(false);
		if (await page.act(account.id, { action: "primary" })) page.setMoveOffer(currentDefault?.sessions.length ? { fromId: currentDefault.id, toId: account.id } : null);
	}
	async function remove() {
		const done = t(signedIn ? "providerAccounts.signedOutOf" : "providerAccounts.removedAccount", { name });
		if (await page.act(account.id, { action: signedIn ? "sign-out" : "remove", replacementPrimaryId: replacement?.id }, done)) setRemoving(false);
	}
	// A reset cannot be taken back: one attempt per confirmation, then the outcome is stated and usage is read again.
	async function spendReset() {
		const result = await page.act(account.id, { action: "reset" });
		if (result) page.say(t(RESET_OUTCOMES[result.resetOutcome ?? "unknown"], { name }));
		setResetting(false);
		await page.recheck();
	}
	function rename(value: string) {
		const displayName = value.trim();
		if (displayName && displayName !== name) void page.act(account.id, { action: "rename", displayName }, t("providerAccounts.nameUpdated"));
	}
	function nameKey(event: KeyboardEvent<HTMLInputElement>) {
		if (event.key === "Escape") event.currentTarget.value = name;
		if (event.key === "Enter" || event.key === "Escape") event.currentTarget.blur();
	}
	const setRule = (change: { reserved?: boolean; onLimit?: string; warnAt?: number }) => void page.act(account.id, { action: "settings", ...change });
	const onLimit = others.find((other) => other.id === account.onLimit);
	const warn = (percent: number) => (percent ? t("providerAccounts.warnAtPercent", { percent }) : t("providerAccounts.never"));
	const windows = usage?.windows ?? [];
	const paused = upcoming(usage?.pausedUntil);
	const extra = usage?.extraUsage;
	const money = (cents: number) => new Intl.NumberFormat(language, { style: "currency", currency: "USD" }).format(cents / 100);
	const resetCount = usage?.resetCredits;
	const resetBlocked = upcoming(usage?.resetBlockedUntil);
	const tokens = usage?.tokens;
	const requests = usage?.requests ?? [];
	const failed = requests.reduce((sum, slice) => sum + slice.failed, 0);
	// The narrow column: what the plan is, then what the account has been doing. A missing figure is left out.
	const facts = (rows: [string, ReactNode][]) => rows.filter(([, value]) => value).map(([label, value]) => <Fact key={label} label={label}>{value}</Fact>);
	const figure = (value?: number | null, unit?: "day") => (typeof value !== "number" ? null : unit ? formatSpan(value, unit, language) : formatCount(value, language));
	const planFacts = facts([
		[t("providerAccounts.planHeading"), plan],
		[t("providerAccounts.factRenews"), usage?.renewsAt ? formatWhen(usage.renewsAt, "day") : null],
		[t("providerAccounts.factOrganization"), usage?.organization ? <span className="max-w-36 truncate" title={usage.organization}>{usage.organization}</span> : null],
		[t("providerAccounts.factAdded"), usage?.addedAt ? formatWhen(usage.addedAt, "day") : null],
		[t("providerAccounts.factModels"), usage?.models?.length ? (
			<span className="flex max-w-44 flex-wrap justify-end gap-1">
				{usage.models.slice(0, 6).map((model) => <span key={model} className="rounded-[5px] bg-muted px-1.5 py-px text-2xs">{model}</span>)}
				{usage.models.length > 6 ? <span title={usage.models.slice(6).join(", ")} className="rounded-[5px] bg-muted px-1.5 py-px text-2xs text-muted-foreground">+{usage.models.length - 6}</span> : null}
			</span>
		) : null],
	]);
	const tokenFacts = tokens ? facts([
		[tokens.latestDay && isToday(tokens.latestDay) ? t("providerAccounts.tokensToday") : t("providerAccounts.tokensOnDay", { date: formatWhen(`${tokens.latestDay}T12:00:00`, "day") }), tokens.latestDay ? figure(tokens.latestDayTokens) : null],
		[t("providerAccounts.tokensLifetime"), figure(tokens.lifetime)],
		[t("providerAccounts.tokensPeakDay"), figure(tokens.peakDaily)],
		[t("providerAccounts.longestTurn"), tokens.longestTurnSeconds ? formatTurn(tokens.longestTurnSeconds, language) : null],
		[t("providerAccounts.currentStreak"), figure(tokens.currentStreakDays, "day")],
		[t("providerAccounts.longestStreak"), figure(tokens.longestStreakDays, "day")],
	]) : [];
	// What the helper counted is known even while the provider is not answering.
	const activity = requests.length > 0 || tokenFacts.length > 0 || Boolean(account.usage?.activity);
	const signInHint = !signedIn ? (count ? t("providerAccounts.sessionsWaitingFor", { count }) : t("providerAccounts.signInToUse"))
		: account.usage?.signInEndsAt ? t("providerAccounts.signInEndsAt", { time: formatWhen(account.usage.signInEndsAt, "soon") }) : t("providerAccounts.signInEndsSoon");
	const twoColumns = signedIn && (planFacts.length > 0 || activity);
	// A sign-in the helper holds can be replaced by a fresh one from its provider.
	const renewable = signedIn && !apiKey && Boolean(usage?.refreshedAt || usage?.addedAt || usage?.requests);
	const health = account.usage?.health;
	const models = account.usage?.activity?.models ?? [];
	const modelTokens = models.reduce((sum, model) => sum + model.tokens, 0);
	const share = (model: { tokens: number }) => Math.round((model.tokens / modelTokens) * 100);
	return (
		<div data-testid="provider-account-detail">
			<div className="flex min-h-11 items-center gap-3.5">
				<AgentAvatar className="size-10 shrink-0" decorative provider={info.agent} />
				<div className="min-w-0 flex-1">
					<div className="flex items-center gap-2.5">
						<h3 className="truncate text-lg font-semibold tracking-tight text-foreground">{name}</h3>
						{account.global ? <DeviceTag apiKey={apiKey} testId="provider-account-global" /> : null}
					</div>
					<p className="flex flex-wrap items-center gap-x-1.5 text-sm text-muted-foreground">
						<span>{info.name}</span>
						{apiKey || plan ? <><Dot /><span>{apiKey ? t("providerAccounts.apiKeyLabel") : plan}</span></> : null}
						{signedIn ? null : <><Dot /><span className="text-status-needs-you">{t("settings.harness.notLoggedIn")}</span></>}
					</p>
				</div>
				{signedIn && !apiKey ? <Button type="button" variant="ghost" size="sm" className="gap-1.5 text-muted-foreground" onClick={() => void page.run(() => aoBridge.app.openExternal(PLAN_PAGES[account.provider]))}>{t("providerAccounts.managePlan")}<ExternalLink aria-hidden="true" className="size-3.5" /></Button> : null}
				{account.primary && signedIn ? <span className="shrink-0 rounded-full bg-interactive-active px-2.5 py-0.5 text-xs text-muted-foreground">{t("providerAccounts.default")}</span> : null}
			</div>
			<div className={cn("grid items-start gap-x-7", twoColumns ? "grid-cols-[minmax(0,1fr)_288px] @max-5xl:grid-cols-1" : "")}>
				<div className="min-w-0">
					{/* Signed out, or a sign-in that still works but has stopped renewing: the way back in comes first. */}
					{!signedIn || account.usage?.signInEnding || signingIn ? (
						<Rows title={signedIn ? undefined : t("shell.signIn")} data-testid={signedIn ? `provider-account-sign-in-ending-${account.id}` : undefined} className={signedIn ? "mt-4" : undefined}>
							{signingIn ? <div className="px-4 py-3.5"><LoginProgress login={signingIn} page={page} /></div> : (
								<Row icon={signedIn ? <AlertCircle aria-hidden="true" className="size-4 shrink-0 text-warning" /> : undefined} title={t(signedIn ? "providerAccounts.signInEnding" : "providerAccounts.signedOutTitle")} hint={signInHint}>
									<Button type="button" variant={signedIn ? "secondary" : "primary"} disabled={page.pending || page.signIn.waiting} onClick={() => void page.run(() => page.signIn.start({ provider: account.provider, accountId: account.id }))}>{t("providerAccounts.signInAgain")}</Button>
								</Row>
							)}
						</Rows>
					) : null}
					{signedIn ? (
						<>
							<Rows title={t("providerAccounts.limitsHeading")}>
								{paused ? (
									<Row icon={<CirclePause aria-hidden="true" className="size-4 shrink-0 text-warning" />} title={t("providerAccounts.pausedUntil", { time: formatWhen(paused, "soon") })} hint={t(usage?.pausedReason === "quota" || usage?.pausedReason === "credential_quota" ? "providerAccounts.pausedRateLimited" : "providerAccounts.pausedRefused")}>
										<Button type="button" variant="secondary" disabled={page.pending} onClick={() => void page.act(account.id, { action: "resume" }, t("providerAccounts.accountResumed", { name }))}>{t("providerAccounts.resumeNow")}</Button>
									</Row>
								) : null}
								{windows.map((window, index) => {
									const seconds = window.durationSeconds ?? 0;
									const length = seconds === 7 * DAY ? t("automations.schedule.weekly")
										: seconds >= DAY && seconds % DAY === 0 ? t("providerAccounts.usageWindowDays", { days: seconds / DAY })
											: seconds >= 3600 ? t("providerAccounts.usageWindowHours", { hours: Math.round(seconds / 3600) }) : "";
									const scope = window.scope && window.scope !== "model" ? t(SCOPES[window.scope]) : window.name ?? "";
									const title = scope || length || t("providerAccounts.usageLabel");
									const hint = [scope ? length : "", window.resetTime ? t("providerAccounts.usageResets", { reset: formatWhen(window.resetTime, "time") }) : ""].filter(Boolean).join(" · ");
									const meterLabel = t("providerAccounts.usageMeterLabel", { account: account.email, window: [scope, length].filter(Boolean).join(" · ") || title });
									return <MeterRow key={index} testId={`provider-account-usage-${account.id}${index ? `-${index}` : ""}`} title={title} hint={hint} percent={percentLeft(window.remainingFraction)} meterLabel={meterLabel} />;
								})}
								{windows.length ? null : <div data-testid={`provider-account-usage-${account.id}`}><Row title={t(!account.usage ? "providerAccounts.loading" : apiKey ? "providerAccounts.usageNotReported" : "providerAccounts.usageUnavailable")} /></div>}
								{!extra ? null : extra.limitCents > 0 ? (
									<MeterRow
										title={t("providerAccounts.extraUsage")}
										hint={t("providerAccounts.extraUsageSpent", { used: money(extra.usedCents), limit: money(extra.limitCents) })}
										percent={percentLeft(1 - extra.usedCents / extra.limitCents)}
										value={t("providerAccounts.amountLeft", { amount: money(Math.max(0, extra.limitCents - extra.usedCents)) })}
									/>
								) : <Row title={t("providerAccounts.extraUsage")} hint={t("providerAccounts.extraUsageSpentNoCap", { used: money(extra.usedCents) })} />}
								{usage?.credits ? (
									<Row title={t("providerAccounts.credits")} hint={t("providerAccounts.creditsHint")}>
										<span className="text-sm tabular-nums text-foreground">{usage.credits.unlimited ? t("providerAccounts.creditsUnlimited") : t("providerAccounts.creditsAmount", { amount: new Intl.NumberFormat(language, { maximumFractionDigits: 0 }).format(Number(usage.credits.balance)) })}</span>
									</Row>
								) : null}
							</Rows>
							{typeof resetCount === "number" || (!apiKey && usage) ? (
								<Rows title={t("providerAccounts.resetsHeading")}>
									{!resetCount || resetCount <= 0 ? <Row title={t("providerAccounts.noResets")} /> : resetting ? (
										<Row label={t("providerAccounts.useReset")} title={t("providerAccounts.confirmUseReset", { name })} hint={t(usage?.resetUsable ? "providerAccounts.confirmUseResetHint" : "providerAccounts.confirmUseResetEarly")}>
											<Button type="button" variant="ghost" className="text-muted-foreground" disabled={page.pending} onClick={() => setResetting(false)}>{t("confirm.cancel")}</Button>
											<Button type="button" disabled={page.pending} onClick={() => void spendReset()}>{t("providerAccounts.useReset")}</Button>
										</Row>
									) : (
										<Row title={t("providerAccounts.limitResets")} hint={usage?.resetUsable ? t("providerAccounts.resetUsableHint") : resetBlocked ? t("providerAccounts.resetBlockedHint", { time: formatWhen(resetBlocked, "soon") }) : t("providerAccounts.resetIdleHint")}>
											<span className="text-sm tabular-nums text-foreground">{t("providerAccounts.resetsAvailableShort", { count: resetCount ?? 0 })}</span>
											<Button type="button" variant={usage?.resetUsable ? "primary" : "secondary"} disabled={page.pending || Boolean(resetBlocked)} onClick={() => setResetting(true)}>{t("providerAccounts.useReset")}</Button>
										</Row>
									)}
									{(resetCount && resetCount > 0 ? usage?.resets ?? [] : []).map((reset, index) => (
										<Fact key={index} label={[reset.label || t("providerAccounts.resetItem", { number: index + 1 }), reset.total > 1 ? t("providerAccounts.resetLeftOf", { left: reset.left, total: reset.total }) : ""].filter(Boolean).join(" · ")}>
											{reset.expiresAt ? t("providerAccounts.resetExpires", { date: formatWhen(reset.expiresAt, "day") }) : null}
										</Fact>
									))}
								</Rows>
							) : null}
							<Rows title={t("providerAccounts.sessionsHeading")}>
								<Row title={t("providerAccounts.newSessionsTitle", { agent: info.agentName })} hint={account.primary ? t("providerAccounts.startHere") : currentDefault ? t("providerAccounts.startOn", { name: currentDefault.displayName }) : undefined}>
									{account.primary
										? <span className="flex items-center gap-1.5 text-sm text-muted-foreground"><Check aria-hidden="true" className="size-3.5 text-status-ready" />{t("providerAccounts.default")}</span>
										: <Button type="button" variant="secondary" disabled={page.pending} onClick={() => void makeDefault()}>{t("providerAccounts.makeDefault")}</Button>}
								</Row>
								{offerFrom?.sessions.length ? (
									<Row label={t("providerAccounts.moveSessions")} title={t("providerAccounts.sessionsStillUse", { count: offerFrom.sessions.length, name: offerFrom.displayName })} hint={t("providerAccounts.offerHint")}>
										<Button type="button" variant="secondary" disabled={page.pending} onClick={() => page.moveSessions(offerFrom, account)}>{t("providerAccounts.moveSessionsHere", { count: offerFrom.sessions.length })}</Button>
										<IconAction name={t("providerAccounts.leaveSessions", { count: offerFrom.sessions.length })} icon={X} disabled={page.pending} onClick={() => page.setMoveOffer(null)} />
									</Row>
								) : null}
								<Row title={t("providerAccounts.runningSessions")} hint={count ? t("providerAccounts.sessionsUsing", { count }) : t("providerAccounts.noSessionsUsing")}>
									{count && others.length ? (
										<AccountMenu accounts={others} onSelect={(target) => page.moveSessions(account, target)}>
											<Button type="button" variant="secondary" className="gap-1.5" disabled={page.pending}>{t("providerAccounts.moveSessions")}<ChevronDown aria-hidden="true" className="size-3.5 text-muted-foreground" /></Button>
										</AccountMenu>
									) : null}
								</Row>
								{/* What the account does without being asked: take new sessions, hand its sessions on at a limit, warn before one. */}
								<Row title={t("providerAccounts.useForNew")} hint={t(account.primary ? "providerAccounts.useForNewDefault" : "providerAccounts.useForNewHint")}>
									<Switch aria-label={t("providerAccounts.useForNew")} checked={!account.reserved} disabled={page.pending || account.primary} onCheckedChange={(on) => setRule({ reserved: !on })} />
								</Row>
								{others.length ? (
									<Row title={t("providerAccounts.onLimit")} hint={t("providerAccounts.onLimitHint")}>
										<Choice label={onLimit ? t("providerAccounts.switchTo", { name: onLimit.displayName }) : t("providerAccounts.doNothing")}>
											<DropdownMenuItem onSelect={() => setRule({ onLimit: "" })}><Tick on={!onLimit} />{t("providerAccounts.doNothing")}</DropdownMenuItem>
											<AccountMenuItems accounts={others} selectedId={onLimit?.id ?? ""} markDefault={false} onSelect={(other) => setRule({ onLimit: other.id })} />
										</Choice>
									</Row>
								) : null}
								<Row title={t("providerAccounts.warnWhenLow")}>
									<Choice label={warn(account.warnAt ?? 0)}>
										{[0, 5, 10, 20, 30].map((percent) => <DropdownMenuItem key={percent} onSelect={() => setRule({ warnAt: percent })}><Tick on={(account.warnAt ?? 0) === percent} />{warn(percent)}</DropdownMenuItem>)}
									</Choice>
								</Row>
							</Rows>
							<AccountSessions account={account} page={page} others={others} language={language} />
							{/* How the account's recent requests went, as the helper saw them. */}
							{health?.lastFailure || health?.firstWordMs ? (
								<Rows title={t("providerAccounts.healthHeading")}>
									{health.lastFailure ? (
										<Row icon={<AlertCircle aria-hidden="true" className="size-4 shrink-0 text-warning" />} title={t("providerAccounts.lastFailure")} hint={`${t(FAILURES[health.lastFailure.kind])} · ${new Intl.DateTimeFormat(language, { dateStyle: "medium", timeStyle: "short" }).format(new Date(health.lastFailure.at))}`}>
											<span className="flex gap-2.5 text-[13px] tabular-nums text-muted-foreground">
												{(["limit", "signIn", "server", "other"] as const).filter((kind) => health.failures?.[kind]).map((kind) => <span key={kind}><span className="font-medium text-foreground">{health.failures![kind]}</span> {t(FAILURES[kind === "signIn" ? "sign-in" : kind]).toLowerCase()}</span>)}
											</span>
										</Row>
									) : null}
									{health.firstWordMs ? (
										<Row title={t("providerAccounts.firstWord")} hint={t("providerAccounts.firstWordHint")}>
											<span className="text-sm tabular-nums text-foreground">{t("providerAccounts.seconds", { value: new Intl.NumberFormat(language, { maximumFractionDigits: 1 }).format(health.firstWordMs / 1000) })}</span>
										</Row>
									) : null}
								</Rows>
							) : null}
						</>
					) : null}
					<Rows title={t("providerAccounts.account")}>
						<Row title={t("cloudLocalAuth.displayName")} hint={t("providerAccounts.displayNameHint")}>
							{/* Keyed by the saved name, so a refused rename shows it again. Escape belongs to this field, not to the settings page. */}
							<input key={name} data-settings-inline-edit="" className="h-8 w-[280px] max-w-full rounded-md border border-input bg-background px-2.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring" aria-label={t("providerAccounts.accountName")} defaultValue={name} disabled={page.pending} onBlur={(event) => rename(event.target.value)} onKeyDown={nameKey} />
						</Row>
						<Row title={t(apiKey ? "providerAccounts.keyLabel" : "providerAccounts.signedInAs")} hint={t(apiKey ? "providerAccounts.keyLabelHint" : "providerAccounts.emailHint")}>
							{/* Blurred until pointed at or focused, so a shared screen does not show it. */}
							<span tabIndex={0} title={account.email} className="max-w-[280px] truncate rounded-sm text-sm text-foreground blur-sm outline-none transition-[filter] hover:blur-none focus:blur-none">{account.email}</span>
						</Row>
						{renewable ? (
							<Row title={t("providerAccounts.renewSignIn")} hint={[t("providerAccounts.renewSignInHint", { provider: info.name }), usage?.refreshedAt ? t("providerAccounts.lastRenewed", { when: formatAgo(new Date(usage.refreshedAt).getTime(), language, t("time.justNow")) }) : null].filter(Boolean).join(" ")}>
								<Button type="button" variant="secondary" disabled={page.pending} onClick={() => void page.act(account.id, { action: "refresh-sign-in" }, t("providerAccounts.signInRenewed"))}>{t("providerAccounts.renew")}</Button>
							</Row>
						) : null}
						{removing ? (
							<Row label={t("providerAccounts.confirmChange")} title={t(signedIn ? "providerAccounts.confirmSignOut" : "providerAccounts.confirmRemove", { email: name })} hint={removalFact}>
								{replacement ? (
									<Choice label={replacement.displayName} menuClassName="min-w-60" aria-label={t("providerAccounts.replacementPrimary")} disabled={page.pending}>
										<AccountMenuItems accounts={others} selectedId={replacement.id} markDefault={false} onSelect={(other) => setPicked(other.id)} />
									</Choice>
								) : null}
								<Button type="button" variant="ghost" className="text-muted-foreground" disabled={page.pending} onClick={() => setRemoving(false)}>{t("confirm.cancel")}</Button>
								<Button type="button" variant="ghost" className={danger} disabled={page.pending} onClick={() => void remove()}>{t(signedIn ? "shell.signOut" : "shell.remove")}</Button>
							</Row>
						) : (
							<Row title={signedIn ? t("shell.signOut") : t("providerAccounts.removeAccount")} hint={t(signedIn ? "providerAccounts.signOutHint" : "providerAccounts.removeHint")}>
								<Button type="button" variant="ghost" className={danger} disabled={page.pending} onClick={() => setRemoving(true)}>{t(signedIn ? "shell.signOut" : "shell.remove")}</Button>
							</Row>
						)}
					</Rows>
				</div>
				{twoColumns ? (
					<aside className="min-w-0">
						{planFacts.length ? <Rows title={t(apiKey ? "providerAccounts.apiKeyLabel" : "providerAccounts.planHeading")}>{planFacts}</Rows> : null}
						{activity ? (
							<Rows title={t("providerAccounts.activityHeading")}>
								<AccountThroughput account={account} language={language} />
								{requests.length ? (
									<Row title={t("providerAccounts.requests")} hint={t("providerAccounts.requestsWindow")}>
										<div className="text-right">
											<p className="text-sm tabular-nums text-foreground">{new Intl.NumberFormat(language).format(requests.reduce((sum, slice) => sum + slice.succeeded, failed))}</p>
											<p className="mt-px text-xs tabular-nums text-muted-foreground">{failed ? t("providerAccounts.requestsFailed", { failed }) : t("providerAccounts.requestsNoneFailed")}</p>
										</div>
									</Row>
								) : null}
								{tokenFacts}
							</Rows>
						) : null}
						{modelTokens ? (
							<Rows title={t("providerAccounts.byModel")}>
								<div className="grid gap-2 px-4 py-3">
									{models.map((model) => (
										<div key={model.model} className="grid grid-cols-[minmax(0,1fr)_72px_36px] items-center gap-2.5 text-[13px]">
											<span className="truncate text-foreground" title={model.model}>{model.model}</span>
											<span className="h-1.5 overflow-hidden rounded-full bg-muted"><span className="block h-full rounded-full bg-muted-foreground" style={{ width: `${share(model)}%` }} /></span>
											<span className="text-right tabular-nums text-muted-foreground">{share(model)}%</span>
										</div>
									))}
								</div>
							</Rows>
						) : null}
					</aside>
				) : null}
			</div>
		</div>
	);
}
