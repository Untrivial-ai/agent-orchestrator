// Route only the landing aliases here; other AO services have separate origins.
const aliases = new Set([
  "aoagents.dev",
  "www.aoagents.dev",
  "useao.dev",
  "www.useao.dev",
  "www.orchestrator.inc",
]);

export default {
  fetch(request) {
    const url = new URL(request.url);
    if (!aliases.has(url.hostname)) {
      return new Response("Not found", { status: 404 });
    }
    url.protocol = "https:";
    url.host = "orchestrator.inc";
    url.port = "";
    return Response.redirect(url.href, 308);
  },
};
