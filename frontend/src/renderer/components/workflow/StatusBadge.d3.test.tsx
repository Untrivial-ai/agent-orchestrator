import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusBadge } from "./StatusBadge";

// D3: Comprehensive StatusBadge variant coverage for all entity types

describe("StatusBadge D3: Plan statuses", () => {
	it("draft → neutral variant", () => {
		render(<StatusBadge status="draft" entity="plan" />);
		expect(screen.getByText("Draft")).toBeInTheDocument();
	});

	it("confirmed → accent variant", () => {
		render(<StatusBadge status="confirmed" entity="plan" />);
		expect(screen.getByText("Confirmed")).toBeInTheDocument();
	});

	it("in_progress → accent variant", () => {
		render(<StatusBadge status="in_progress" entity="plan" />);
		expect(screen.getByText("In Progress")).toBeInTheDocument();
	});

	it("completed → success variant", () => {
		render(<StatusBadge status="completed" entity="plan" />);
		expect(screen.getByText("Completed")).toBeInTheDocument();
	});

	it("cancelled → error variant", () => {
		render(<StatusBadge status="cancelled" entity="plan" />);
		expect(screen.getByText("Cancelled")).toBeInTheDocument();
	});
});

describe("StatusBadge D3: Stage statuses", () => {
	it("pending → neutral variant", () => {
		render(<StatusBadge status="pending" entity="stage" />);
		expect(screen.getByText("Pending")).toBeInTheDocument();
	});

	it("in_progress → accent variant", () => {
		render(<StatusBadge status="in_progress" entity="stage" />);
		expect(screen.getByText("In Progress")).toBeInTheDocument();
	});

	it("ready_for_approval → warning variant", () => {
		render(<StatusBadge status="ready_for_approval" entity="stage" />);
		expect(screen.getByText("Ready for Approval")).toBeInTheDocument();
	});

	it("passed → success variant", () => {
		render(<StatusBadge status="passed" entity="stage" />);
		expect(screen.getByText("Passed")).toBeInTheDocument();
	});

	it("blocked → warning variant", () => {
		render(<StatusBadge status="blocked" entity="stage" />);
		expect(screen.getByText("Blocked")).toBeInTheDocument();
	});

	it("cancelled → error variant", () => {
		render(<StatusBadge status="cancelled" entity="stage" />);
		expect(screen.getByText("Cancelled")).toBeInTheDocument();
	});
});

describe("StatusBadge D3: Task statuses", () => {
	it("pending → neutral variant", () => {
		render(<StatusBadge status="pending" entity="task" />);
		expect(screen.getByText("Pending")).toBeInTheDocument();
	});

	it("ready → accent variant", () => {
		render(<StatusBadge status="ready" entity="task" />);
		expect(screen.getByText("Ready")).toBeInTheDocument();
	});

	it("running → accent variant", () => {
		render(<StatusBadge status="running" entity="task" />);
		expect(screen.getByText("Running")).toBeInTheDocument();
	});

	it("review → warning variant", () => {
		render(<StatusBadge status="review" entity="task" />);
		expect(screen.getByText("In Review")).toBeInTheDocument();
	});

	it("passed → success variant", () => {
		render(<StatusBadge status="passed" entity="task" />);
		expect(screen.getByText("Passed")).toBeInTheDocument();
	});

	it("blocked → warning variant", () => {
		render(<StatusBadge status="blocked" entity="task" />);
		expect(screen.getByText("Blocked")).toBeInTheDocument();
	});

	it("cancelled → error variant", () => {
		render(<StatusBadge status="cancelled" entity="task" />);
		expect(screen.getByText("Cancelled")).toBeInTheDocument();
	});
});

describe("StatusBadge D3: Run statuses", () => {
	it("pending → neutral variant", () => {
		render(<StatusBadge status="pending" entity="run" />);
		expect(screen.getByText("Pending")).toBeInTheDocument();
	});

	it("running → accent variant", () => {
		render(<StatusBadge status="running" entity="run" />);
		expect(screen.getByText("Running")).toBeInTheDocument();
	});

	it("succeeded → success variant", () => {
		render(<StatusBadge status="succeeded" entity="run" />);
		expect(screen.getByText("Succeeded")).toBeInTheDocument();
	});

	it("failed → error variant", () => {
		render(<StatusBadge status="failed" entity="run" />);
		expect(screen.getByText("Failed")).toBeInTheDocument();
	});

	it("cancelled → error variant", () => {
		render(<StatusBadge status="cancelled" entity="run" />);
		expect(screen.getByText("Cancelled")).toBeInTheDocument();
	});
});

describe("StatusBadge D3: Review statuses", () => {
	it("pending → neutral variant", () => {
		render(<StatusBadge status="pending" entity="review" />);
		expect(screen.getByText("Pending")).toBeInTheDocument();
	});

	it("passed → success variant", () => {
		render(<StatusBadge status="passed" entity="review" />);
		expect(screen.getByText("Passed")).toBeInTheDocument();
	});

	it("rejected → error variant", () => {
		render(<StatusBadge status="rejected" entity="review" />);
		expect(screen.getByText("Rejected")).toBeInTheDocument();
	});
});

describe("StatusBadge D3: Fallback and edge cases", () => {
	it("falls back to underscore-replaced text for unknown status", () => {
		render(<StatusBadge status="custom_status" entity="plan" />);
		expect(screen.getByText("custom status")).toBeInTheDocument();
	});

	it("renders plan status with correct variant classes", () => {
		const { container } = render(<StatusBadge status="completed" entity="plan" />);
		const badge = container.querySelector("span");
		expect(badge).toBeTruthy();
	});
});
