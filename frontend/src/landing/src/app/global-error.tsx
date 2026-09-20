"use client";

import NextError from "next/error";
import { useEffect } from "react";

export default function GlobalError({
	error,
}: {
	error: Error & { digest?: string };
}) {
	useEffect(() => {
		// Imported lazily: a static import pulls the whole @sentry/nextjs browser
		// SDK (~60 KB br) into the first-load bundle of every route, to report a
		// crash that almost never happens. The dynamic import costs one request on
		// the error path only.
		import("@sentry/nextjs").then((Sentry) => {
			Sentry.captureException(error);
		});
	}, [error]);

	return (
		<html lang="en">
			<body>
				<NextError statusCode={0} />
			</body>
		</html>
	);
}
