import { FileIcon, FolderIcon } from "@react-symbols/icons/utils";
import { cn } from "../lib/utils";

type WorkspaceEntryIconProps = {
	kind: "file" | "dir";
	name: string;
	className?: string;
	testId?: string;
};

export function WorkspaceEntryIcon({ kind, name, className, testId }: WorkspaceEntryIconProps) {
	const iconClassName = cn("size-icon-md shrink-0", className);
	if (kind === "dir") {
		return (
			<FolderIcon
				aria-hidden="true"
				className={iconClassName}
				data-testid={testId}
				folderName={name.toLowerCase()}
			/>
		);
	}
	return <FileIcon aria-hidden="true" autoAssign className={iconClassName} data-testid={testId} fileName={name} />;
}
