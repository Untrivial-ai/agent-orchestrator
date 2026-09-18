export type WorkerRenameAction = {
	id: "rename";
	title: string;
};

export function workerRenameActions(): WorkerRenameAction[] {
	return [{ id: "rename", title: "Rename" }];
}
