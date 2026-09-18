import TerminalSessionScreen from "../../lib/session/TerminalSessionScreen";

/**
 * A Chat session's terminal escape hatch attaches the shell handle through the
 * same authenticated mux and xterm renderer as every other AO terminal. The
 * handle, not the Chat session id, identifies this PTY.
 */
export default TerminalSessionScreen;

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
