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

	class Database {
		constructor(filename: string, options?: DatabaseOptions);
		pragma(source: string): unknown;
		backup(filename: string): Promise<unknown>;
		exec(source: string): this;
		prepare(source: string): Statement;
		close(): void;
	}

	export default Database;
}
