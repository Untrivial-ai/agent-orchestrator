import { mdxComponents } from "@/app/blog/components/mdx-components";
import { MediaPlaceholder } from "../MediaPlaceholder";
import { PRBadge } from "../PRBadge";

export const changelogMdxComponents = {
	...mdxComponents,
	PRBadge,
	MediaPlaceholder,
	ul: ({ children, ...props }: React.HTMLAttributes<HTMLUListElement>) => (
		<ul className="list-disc list-outside pl-5 space-y-1" {...props}>
			{children}
		</ul>
	),
	li: ({ children, ...props }: React.HTMLAttributes<HTMLLIElement>) => (
		<li {...props}>{children}</li>
	),
};
