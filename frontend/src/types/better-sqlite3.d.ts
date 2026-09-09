declare module "better-sqlite3" {
	type DatabaseOptions = {
		readonly?: boolean;
		fileMustExist?: boolean;
		timeout?: number;
	};

	type Statement = {
		all: (...parameters: unknown[]) => unknown[];
		get: (...parameters: unknown[]) => unknown;
		run: (...parameters: unknown[]) => unknown;
	};

	type BackupResult = {
		totalPages: number;
		remainingPages: number;
	};

	class Database {
		constructor(filename: string, options?: DatabaseOptions);
		pragma(source: string): unknown;
		backup(filename: string): Promise<BackupResult>;
		exec(source: string): this;
		prepare(source: string): Statement;
		close(): void;
	}

	export default Database;
}
