/**
 * Shared cloud-session target for inspector data planes that need an org +
 * control-plane client. Kept tiny so Files/SCM/Browser hooks can share one
 * optional prop without importing React.
 */

export type CloudInspectorTarget = {
	orgId: string;
	/** Control-plane origin used for browser-proxy absolute URLs. */
	baseUrl: string;
};
