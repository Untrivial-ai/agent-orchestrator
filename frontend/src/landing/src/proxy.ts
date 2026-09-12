import { authkitMiddleware } from "@workos-inc/authkit-nextjs";
import { NextResponse, type NextMiddleware } from "next/server";

const usesWorkOS =
	process.env.NEXT_PUBLIC_AO_AUTH_MODE === "workos" ||
	process.env.AO_CLOUD_AUTH_MODE === "workos";

// Local Cloud auth is handled by the Go control plane. Do not initialize the
// AuthKit middleware in that mode: it requires WorkOS credentials even though
// local auth intentionally has none.
const localMiddleware: NextMiddleware = () => NextResponse.next();

export default usesWorkOS ? authkitMiddleware() : localMiddleware;
