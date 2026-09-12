import { beforeEach, describe, expect, it } from "vitest";
import { useWorkflowStore } from "./workflow-store";

describe("useWorkflowStore", () => {
	beforeEach(() => {
		useWorkflowStore.getState().reset();
	});

	it("has correct initial state", () => {
		const state = useWorkflowStore.getState();
		expect(state.selectedPlanId).toBeNull();
		expect(state.selectedTaskId).toBeNull();
		expect(state.isTaskDetailOpen).toBe(false);
		expect(state.planFilter).toBe("all");
		expect(state.planSearch).toBe("");
	});

	it("sets selectedPlanId", () => {
		useWorkflowStore.getState().setSelectedPlanId("plan-1");
		expect(useWorkflowStore.getState().selectedPlanId).toBe("plan-1");
	});

	it("sets selectedTaskId", () => {
		useWorkflowStore.getState().setSelectedTaskId("task-1");
		expect(useWorkflowStore.getState().selectedTaskId).toBe("task-1");
	});

	it("openTaskDetail sets both selectedTaskId and isTaskDetailOpen", () => {
		useWorkflowStore.getState().openTaskDetail("task-2");
		const state = useWorkflowStore.getState();
		expect(state.selectedTaskId).toBe("task-2");
		expect(state.isTaskDetailOpen).toBe(true);
	});

	it("closeTaskDetail clears selectedTaskId and isTaskDetailOpen", () => {
		useWorkflowStore.getState().openTaskDetail("task-2");
		useWorkflowStore.getState().closeTaskDetail();
		const state = useWorkflowStore.getState();
		expect(state.selectedTaskId).toBeNull();
		expect(state.isTaskDetailOpen).toBe(false);
	});

	it("sets planFilter", () => {
		useWorkflowStore.getState().setPlanFilter("in_progress");
		expect(useWorkflowStore.getState().planFilter).toBe("in_progress");
	});

	it("sets planSearch", () => {
		useWorkflowStore.getState().setPlanSearch("my plan");
		expect(useWorkflowStore.getState().planSearch).toBe("my plan");
	});

	it("reset restores initial state", () => {
		useWorkflowStore.getState().setSelectedPlanId("p1");
		useWorkflowStore.getState().setSelectedTaskId("t1");
		useWorkflowStore.getState().openTaskDetail("t1");
		useWorkflowStore.getState().setPlanFilter("draft");
		useWorkflowStore.getState().setPlanSearch("search");

		useWorkflowStore.getState().reset();

		const state = useWorkflowStore.getState();
		expect(state.selectedPlanId).toBeNull();
		expect(state.selectedTaskId).toBeNull();
		expect(state.isTaskDetailOpen).toBe(false);
		expect(state.planFilter).toBe("all");
		expect(state.planSearch).toBe("");
	});
});
