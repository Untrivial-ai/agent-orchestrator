// Typed failures for the cloud control-plane client. Callers can branch on
// `instanceof CloudCpAuthError` for sign-in-required handling and read
// `status`/`code`/`requestId` (from the control plane's error envelope) off
// any `CloudCpError` for everything else.

export interface CloudCpErrorOptions {
	/** HTTP status of the failed response; 0 when the request never produced one (network failure). */
	status: number;
	/** Machine-readable code from the control plane's error envelope (e.g. "validation_error"). */
	code?: string;
	/** Correlation id from the control plane's error envelope. */
	requestId?: string;
	cause?: unknown;
}

export class CloudCpError extends Error {
	readonly status: number;
	readonly code?: string;
	readonly requestId?: string;

	constructor(message: string, options: CloudCpErrorOptions) {
		super(message, options.cause === undefined ? undefined : { cause: options.cause });
		this.name = "CloudCpError";
		this.status = options.status;
		this.code = options.code;
		this.requestId = options.requestId;
	}
}

/**
 * The control plane rejected the bearer token (HTTP 401), or no token was
 * available to attach. The renderer should treat this as "sign in again",
 * never as a retryable request failure.
 */
export class CloudCpAuthError extends CloudCpError {
	constructor(message: string, options: CloudCpErrorOptions) {
		super(message, options);
		this.name = "CloudCpAuthError";
	}
}
