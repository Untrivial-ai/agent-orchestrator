import { describe, expect, it } from "vitest";
import {
	ALL_WORKER_PROJECTS,
	filterWorkersByProject,
	spawnProjectParam,
	workerProjectLabel,
	workerProjectOptions,
	workerSearchPresentation,
} from "./worker-controls";
import { hostedProjectKey } from "./hostedRows";

describe("filterWorkersByProject", () => {
	it("keeps every worker when All projects is selected", () => {
		const workers = [{ projectId: "alpha" }, { projectId: "beta" }];
		expect(filterWorkersByProject(workers, "all")).toEqual(workers);
	});

	it("keeps only workers from the locally selected project", () => {
		const workers = [{ projectId: "alpha" }, { projectId: "beta" }, { projectId: "alpha" }];
		expect(filterWorkersByProject(workers, "beta")).toEqual([{ projectId: "beta" }]);
	});

	it("does not include another machine's workers when project IDs match", () => {
		const projects = [
			{ id: "same", name: "App", hostId: "a", hostName: "Laptop" },
			{ id: "same", name: "App", hostId: "b", hostName: "Desktop" },
		];
		const workers = [{ projectId: "same", hostId: "a" }, { projectId: "same", hostId: "b" }];
		expect(filterWorkersByProject(workers, hostedProjectKey(projects[1]), projects)).toEqual([workers[1]]);
		expect(spawnProjectParam(hostedProjectKey(projects[1]), projects)).toEqual({ projectId: "same", hostId: "b" });
		expect(workerProjectOptions(projects).map((item) => item.label)).toEqual(["All projects", "App · Laptop", "App · Desktop"]);
	});
});

describe("workerSearchPresentation", () => {
	it("stays expanded while a non-empty query is visible", () => {
		expect(workerSearchPresentation(false, "compiler")).toBe("expanded");
	});

	it("collapses only when search is closed and empty", () => {
		expect(workerSearchPresentation(false, "")).toBe("collapsed");
		expect(workerSearchPresentation(true, "")).toBe("expanded");
	});
});

describe("workerProjectLabel", () => {
	it("uses a stable fallback if a selected project was removed", () => {
		expect(workerProjectLabel([{ id: "alpha", name: "Alpha" }], "missing")).toBe("All projects");
		expect(workerProjectLabel([{ id: "alpha", name: "Alpha" }], "alpha")).toBe("Alpha");
	});
});

describe("workerProjectOptions", () => {
	it("puts the reset option before every available project", () => {
		expect(
			workerProjectOptions([
				{ id: "alpha", name: "Alpha" },
				{ id: "beta", name: "Beta" },
			]),
		).toEqual([
			{ id: "all", label: "All projects" },
			{ id: "alpha", label: "Alpha" },
			{ id: "beta", label: "Beta" },
		]);
	});
});

describe("spawnProjectParam", () => {
	it("carries a concrete Workers project filter into the spawn sheet", () => {
		expect(spawnProjectParam("project-42")).toEqual({ projectId: "project-42" });
	});

	it("leaves project selection open when Workers shows every project", () => {
		expect(spawnProjectParam(ALL_WORKER_PROJECTS)).toBeUndefined();
	});
});
