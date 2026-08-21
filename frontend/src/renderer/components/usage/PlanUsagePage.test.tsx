import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ProviderQuota } from "../../hooks/useProviderQuota";

const hookState = vi.hoisted(() => ({
	providers: [] as ProviderQuota[],
	refreshAll: vi.fn(),
}));

vi.mock("../../hooks/useProviderQuota", () => ({
	useProviderQuota: () => ({
		data: hookState.providers,
		error: null,
		isError: false,
		isLoading: false,
		isSuccess: true,
	}),
	useRefreshAllProviderQuota: () => ({ mutate: hookState.refreshAll }),
}));

vi.mock("../CenterPanelShell", () => ({
	CenterPanelShell: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

const { PlanUsagePage } = await import("./PlanUsagePage");

function quota(overrides: Partial<ProviderQuota> = {}): ProviderQuota {
	return {
		accountId: "default",
		balances: [],
		capabilities: {
			supportsCredits: false,
			supportsHistory: false,
			supportsRead: false,
			supportsSpendLimits: false,
			supportsSubscribe: true,
		},
		completeness: "partial",
		freshness: "fresh",
		limits: [],
		observedAt: new Date().toISOString(),
		provider: "claude",
		severity: "normal",
		...overrides,
	};
}

describe("PlanUsagePage", () => {
	beforeEach(() => {
		hookState.providers = [];
		hookState.refreshAll.mockClear();
	});

	it("shows an actionable empty state before providers report quota", () => {
		render(<PlanUsagePage />);

		expect(screen.getByText("No provider quota observed yet")).toBeInTheDocument();
		expect(screen.getByText(/checking connected providers/i)).toBeInTheDocument();
		expect(hookState.refreshAll).toHaveBeenCalledOnce();
	});

	it("renders Codex and Claude through the same provider-neutral card", () => {
			hookState.providers = [
			quota({
				accountLabel: "Codex Team",
				balances: [{ id: "codex:credits", name: "Codex credits", unlimited: false, value: "50" }],
				capabilities: {
					supportsCredits: true,
					supportsHistory: true,
					supportsRead: true,
					supportsSpendLimits: true,
					supportsSubscribe: true,
				},
				completeness: "complete",
				limits: [{
					category: "requests",
					id: "primary",
					remainingPercent: 8,
					scope: "account",
					severity: "critical",
					usedPercent: 92,
					windowDurationSeconds: 18_000,
					windowType: "rolling",
				}],
				provider: "codex",
				severity: "critical",
			}),
			quota({
				accountLabel: "Claude Pro",
				limits: [{
					category: "requests",
					id: "five_hour",
					remainingPercent: 72,
					scope: "account",
					severity: "normal",
					usedPercent: 28,
					windowDurationSeconds: 18_000,
					windowType: "rolling",
				}],
			}),
		];

		render(<PlanUsagePage />);

		expect(screen.getByText("Codex Team")).toBeInTheDocument();
		expect(screen.getByText("Claude Pro")).toBeInTheDocument();
		expect(screen.getByText("8% remaining")).toHaveClass("text-status-exited");
		expect(screen.getByText("72% remaining")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /refresh/i })).not.toBeInTheDocument();
		expect(screen.queryByText("Credits and balances")).not.toBeInTheDocument();
		expect(screen.queryByText("Observed usage history")).not.toBeInTheDocument();
	});

	it("renders an unknown future provider without frontend adapter code", () => {
		hookState.providers = [quota({ provider: "future-ai", accountLabel: undefined })];

		render(<PlanUsagePage />);

		expect(screen.getByText("Future Ai")).toBeInTheDocument();
	});
});
