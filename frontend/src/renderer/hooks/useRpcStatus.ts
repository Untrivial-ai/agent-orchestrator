import { useEffect } from "react";
import { aoBridge } from "../lib/bridge";
import { useRpcStore } from "../stores/rpc-store";

export function useRpcStatus(): void {
	const setStatus = useRpcStore((state) => state.setStatus);
	useEffect(() => {
		let live = true;
		void aoBridge.rpc.getStatus().then((status) => {
			if (live) setStatus(status);
		});
		const off = aoBridge.rpc.onStatus((status) => {
			if (live) setStatus(status);
		});
		return () => {
			live = false;
			off?.();
		};
	}, [setStatus]);
}
