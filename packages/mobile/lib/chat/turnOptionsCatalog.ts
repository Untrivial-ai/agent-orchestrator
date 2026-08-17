export async function loadTurnOptionCatalog<Models, Options>({
	hasProviderConfig,
	loadModels,
	loadConfigOptions,
}: {
	hasProviderConfig: boolean;
	loadModels(): Promise<Models>;
	loadConfigOptions(): Promise<Options>;
}): Promise<{ models: Models | []; configOptions: Options | [] }> {
	if (hasProviderConfig) {
		return { models: [], configOptions: await loadConfigOptions() };
	}
	return { models: await loadModels(), configOptions: [] };
}
