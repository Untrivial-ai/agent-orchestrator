# MCP smoke

`test/mcp/smoke.sh` builds `ao`, starts a fake loopback daemon, and drives
`ao mcp` over stdio with the official Go MCP client. It asserts the seven
product tools are advertised and that `list_projects` returns the fake
project payload.

```bash
bash test/mcp/smoke.sh
```

Requires Go on PATH. Does not touch a real `~/.ao` install.
