import { create } from "zustand";

type WorkflowState = {
	selectedPlanId: string | null;
	selectedTaskId: string | null;
	isTaskDetailOpen: boolean;
	planFilter: string;
	planSearch: string;
};

type WorkflowActions = {
	setSelectedPlanId: (id: string | null) => void;
	setSelectedTaskId: (id: string | null) => void;
	openTaskDetail: (id: string) => void;
	closeTaskDetail: () => void;
	setPlanFilter: (filter: string) => void;
	setPlanSearch: (search: string) => void;
	reset: () => void;
};

const initialState: WorkflowState = {
	selectedPlanId: null,
	selectedTaskId: null,
	isTaskDetailOpen: false,
	planFilter: "all",
	planSearch: "",
};

export const useWorkflowStore = create<WorkflowState & WorkflowActions>()((set) => ({
	...initialState,
	setSelectedPlanId: (id) => set({ selectedPlanId: id }),
	setSelectedTaskId: (id) => set({ selectedTaskId: id }),
	openTaskDetail: (id) => set({ selectedTaskId: id, isTaskDetailOpen: true }),
	closeTaskDetail: () => set({ selectedTaskId: null, isTaskDetailOpen: false }),
	setPlanFilter: (filter) => set({ planFilter: filter }),
	setPlanSearch: (search) => set({ planSearch: search }),
	reset: () => set(initialState),
}));
