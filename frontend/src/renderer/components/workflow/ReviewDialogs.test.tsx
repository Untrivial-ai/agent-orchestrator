import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { CreateReviewDialog } from "./CreateReviewDialog";
import { RejectDialog } from "./RejectDialog";
import { RetryMenu } from "./RetryMenu";

vi.mock("lucide-react", () => ({
	XIcon: () => null,
}));

vi.mock("react-i18next", () => ({
	useTranslation: () => ({
		t: (key: string, opts?: Record<string, unknown>) => {
			if (key === "common.creating") return "Creating...";
			if (key === "common.close") return "Close";
			return opts?.defaultValue ?? key;
		},
	}),
}));

// ──────────────────────────────────────────────────────────────────────
// CreateReviewDialog
// ──────────────────────────────────────────────────────────────────────
describe("CreateReviewDialog", () => {
	it("renders dialog with source:human label when open", () => {
		render(<CreateReviewDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} />);
		expect(screen.getByTestId("create-review-dialog")).toBeInTheDocument();
		expect(screen.getByText(/sourceHuman/i)).toBeInTheDocument();
	});

	it("does not render dialog when closed", () => {
		render(<CreateReviewDialog open={false} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} />);
		expect(screen.queryByTestId("create-review-dialog")).not.toBeInTheDocument();
	});

	it("renders summary and issues input fields", () => {
		render(<CreateReviewDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} />);
		expect(screen.getByLabelText(/workflow.review.summary/i)).toBeInTheDocument();
		expect(screen.getByLabelText(/workflow.review.issues/i)).toBeInTheDocument();
	});

	it("submits with summary and issues", () => {
		const onSubmit = vi.fn();
		render(<CreateReviewDialog open={true} onOpenChange={() => {}} onSubmit={onSubmit} isPending={false} />);

		fireEvent.change(screen.getByLabelText(/workflow.review.summary/i), { target: { value: "Good work" } });
		fireEvent.change(screen.getByLabelText(/workflow.review.issues/i), { target: { value: "Minor fix" } });
		fireEvent.click(screen.getByTestId("submit-create-review"));

		expect(onSubmit).toHaveBeenCalledWith({ summary: "Good work", issues: "Minor fix" });
	});

	it("submits with undefined when fields are empty", () => {
		const onSubmit = vi.fn();
		render(<CreateReviewDialog open={true} onOpenChange={() => {}} onSubmit={onSubmit} isPending={false} />);

		fireEvent.click(screen.getByTestId("submit-create-review"));
		expect(onSubmit).toHaveBeenCalledWith({ summary: undefined, issues: undefined });
	});

	it("disables submit when isPending", () => {
		render(<CreateReviewDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={true} />);
		expect(screen.getByTestId("submit-create-review")).toBeDisabled();
	});

	it("shows error message when error prop provided", () => {
		render(<CreateReviewDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} error="409 conflict" />);
		expect(screen.getByText("409 conflict")).toBeInTheDocument();
	});

	it("calls onOpenChange(false) when close button clicked", () => {
		const onOpenChange = vi.fn();
		render(<CreateReviewDialog open={true} onOpenChange={onOpenChange} onSubmit={() => {}} isPending={false} />);
		const closeButtons = screen.getAllByRole("button", { name: "Close" });
		fireEvent.click(closeButtons[closeButtons.length - 1]);
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});
});

// ──────────────────────────────────────────────────────────────────────
// RejectDialog
// ──────────────────────────────────────────────────────────────────────
describe("RejectDialog", () => {
	it("renders dialog when open", () => {
		render(<RejectDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} />);
		expect(screen.getByTestId("reject-review-dialog")).toBeInTheDocument();
	});

	it("does not render when closed", () => {
		render(<RejectDialog open={false} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} />);
		expect(screen.queryByTestId("reject-review-dialog")).not.toBeInTheDocument();
	});

	it("renders issues input field", () => {
		render(<RejectDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} />);
		expect(screen.getByLabelText(/workflow.review.rejectIssues/i)).toBeInTheDocument();
	});

	it("submit button is disabled when issues field is empty", () => {
		render(<RejectDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} />);
		expect(screen.getByTestId("submit-reject-review")).toBeDisabled();
	});

	it("submit button is disabled when issues is only whitespace", () => {
		render(<RejectDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} />);
		fireEvent.change(screen.getByLabelText(/workflow.review.rejectIssues/i), { target: { value: "   " } });
		expect(screen.getByTestId("submit-reject-review")).toBeDisabled();
	});

	it("submit button enables when issues has content", () => {
		render(<RejectDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} />);
		fireEvent.change(screen.getByLabelText(/workflow.review.rejectIssues/i), { target: { value: "Bug found" } });
		expect(screen.getByTestId("submit-reject-review")).not.toBeDisabled();
	});

	it("submits with trimmed issues", () => {
		const onSubmit = vi.fn();
		render(<RejectDialog open={true} onOpenChange={() => {}} onSubmit={onSubmit} isPending={false} />);

		fireEvent.change(screen.getByLabelText(/workflow.review.rejectIssues/i), { target: { value: "  Needs fix  " } });
		fireEvent.click(screen.getByTestId("submit-reject-review"));

		expect(onSubmit).toHaveBeenCalledWith("Needs fix");
	});

	it("disables submit when isPending", () => {
		render(<RejectDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={true} />);
		fireEvent.change(screen.getByLabelText(/workflow.review.rejectIssues/i), { target: { value: "Bug" } });
		expect(screen.getByTestId("submit-reject-review")).toBeDisabled();
	});

	it("shows error message when error prop provided", () => {
		render(<RejectDialog open={true} onOpenChange={() => {}} onSubmit={() => {}} isPending={false} error="Server error" />);
		expect(screen.getByText("Server error")).toBeInTheDocument();
	});

	it("calls onOpenChange(false) when close button clicked", () => {
		const onOpenChange = vi.fn();
		render(<RejectDialog open={true} onOpenChange={onOpenChange} onSubmit={() => {}} isPending={false} />);
		const closeButtons = screen.getAllByRole("button", { name: "Close" });
		fireEvent.click(closeButtons[closeButtons.length - 1]);
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});
});

// ──────────────────────────────────────────────────────────────────────
// RetryMenu
// ──────────────────────────────────────────────────────────────────────
describe("RetryMenu", () => {
	it("renders dialog with Resume and Fresh options", () => {
		render(<RetryMenu open={true} onOpenChange={() => {}} onSelect={() => {}} isPending={false} />);
		expect(screen.getByTestId("retry-menu")).toBeInTheDocument();
		expect(screen.getByTestId("retry-resume")).toBeInTheDocument();
		expect(screen.getByTestId("retry-fresh")).toBeInTheDocument();
	});

	it("does not render when closed", () => {
		render(<RetryMenu open={false} onOpenChange={() => {}} onSelect={() => {}} isPending={false} />);
		expect(screen.queryByTestId("retry-menu")).not.toBeInTheDocument();
	});

	it("confirm button disabled when no mode selected", () => {
		render(<RetryMenu open={true} onOpenChange={() => {}} onSelect={() => {}} isPending={false} />);
		expect(screen.getByTestId("confirm-retry")).toBeDisabled();
	});

	it("selects resume mode and enables confirm", () => {
		render(<RetryMenu open={true} onOpenChange={() => {}} onSelect={() => {}} isPending={false} />);
		fireEvent.click(screen.getByTestId("retry-resume"));
		expect(screen.getByTestId("confirm-retry")).not.toBeDisabled();
	});

	it("selects fresh mode and enables confirm", () => {
		render(<RetryMenu open={true} onOpenChange={() => {}} onSelect={() => {}} isPending={false} />);
		fireEvent.click(screen.getByTestId("retry-fresh"));
		expect(screen.getByTestId("confirm-retry")).not.toBeDisabled();
	});

	it("calls onSelect with resume mode on confirm", () => {
		const onSelect = vi.fn();
		render(<RetryMenu open={true} onOpenChange={() => {}} onSelect={onSelect} isPending={false} />);
		fireEvent.click(screen.getByTestId("retry-resume"));
		fireEvent.click(screen.getByTestId("confirm-retry"));
		expect(onSelect).toHaveBeenCalledWith("resume");
	});

	it("calls onSelect with fresh mode on confirm", () => {
		const onSelect = vi.fn();
		render(<RetryMenu open={true} onOpenChange={() => {}} onSelect={onSelect} isPending={false} />);
		fireEvent.click(screen.getByTestId("retry-fresh"));
		fireEvent.click(screen.getByTestId("confirm-retry"));
		expect(onSelect).toHaveBeenCalledWith("fresh");
	});

	it("disables confirm when isPending", () => {
		render(<RetryMenu open={true} onOpenChange={() => {}} onSelect={() => {}} isPending={true} />);
		fireEvent.click(screen.getByTestId("retry-resume"));
		expect(screen.getByTestId("confirm-retry")).toBeDisabled();
	});

	it("calls onOpenChange(false) when close button clicked", () => {
		const onOpenChange = vi.fn();
		render(<RetryMenu open={true} onOpenChange={onOpenChange} onSelect={() => {}} isPending={false} />);
		const closeButtons = screen.getAllByRole("button", { name: "Close" });
		fireEvent.click(closeButtons[closeButtons.length - 1]);
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});
});
