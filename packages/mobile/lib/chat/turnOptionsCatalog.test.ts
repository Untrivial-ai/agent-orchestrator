import { describe, expect, it, vi } from "vitest";
import { loadTurnOptionCatalog } from "./turnOptionsCatalog";

describe("turn option catalog", () => {
	it("loads the capability-specific catalog without swallowing failures", async () => {
		const loadModels = vi.fn().mockResolvedValue([{ id: "gpt" }]);
		const loadConfigOptions = vi.fn();
		expect(await loadTurnOptionCatalog({ hasProviderConfig: false, loadModels, loadConfigOptions }))
			.toEqual({ models: [{ id: "gpt" }], configOptions: [] });
		expect(loadConfigOptions).not.toHaveBeenCalled();

		loadConfigOptions.mockRejectedValueOnce(new Error("catalog unavailable"));
		await expect(loadTurnOptionCatalog({ hasProviderConfig: true, loadModels, loadConfigOptions }))
			.rejects.toThrow("catalog unavailable");
	});
});
