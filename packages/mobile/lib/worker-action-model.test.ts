import { describe, expect, it } from "vitest";
import { workerRenameActions } from "./worker-action-model";


describe("workerRenameActions", () => {
	it("keeps rename as the only long-press action because pinning and deletion are direct rail controls", () => {
		expect(workerRenameActions()).toEqual([{ id: "rename", title: "Rename" }]);
	});
});
