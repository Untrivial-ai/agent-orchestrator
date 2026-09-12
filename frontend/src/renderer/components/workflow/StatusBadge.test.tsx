import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusBadge } from "./StatusBadge";

describe("StatusBadge", () => {
	it("renders plan draft status with translated label", () => {
		render(<StatusBadge status="draft" entity="plan" />);
		expect(screen.getByText("Draft")).toBeInTheDocument();
	});

	it("renders plan in_progress status", () => {
		render(<StatusBadge status="in_progress" entity="plan" />);
		expect(screen.getByText("In Progress")).toBeInTheDocument();
	});

	it("renders stage ready_for_approval status", () => {
		render(<StatusBadge status="ready_for_approval" entity="stage" />);
		expect(screen.getByText("Ready for Approval")).toBeInTheDocument();
	});

	it("renders task running status", () => {
		render(<StatusBadge status="running" entity="task" />);
		expect(screen.getByText("Running")).toBeInTheDocument();
	});

	it("renders run succeeded status", () => {
		render(<StatusBadge status="succeeded" entity="run" />);
		expect(screen.getByText("Succeeded")).toBeInTheDocument();
	});

	it("renders run failed status", () => {
		render(<StatusBadge status="failed" entity="run" />);
		expect(screen.getByText("Failed")).toBeInTheDocument();
	});

	it("falls back to status text with underscores replaced by spaces for unknown statuses", () => {
		render(<StatusBadge status="some_new_status" entity="plan" />);
		expect(screen.getByText("some new status")).toBeInTheDocument();
	});
});
