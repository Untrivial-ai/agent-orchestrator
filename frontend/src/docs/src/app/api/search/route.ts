import { createFromSource } from "fumadocs-core/search/server";
import { source } from "@/lib/source";
import { visibleProductName } from "@/lib/branding";

const search = createFromSource({
  ...source,
  getPages: () => source.getPages().map((page) => ({
    ...page,
    data: {
      ...page.data,
      description: page.data.description && visibleProductName(page.data.description),
    },
  })),
});

export const dynamic = "force-static";

export const GET = search.staticGET;
