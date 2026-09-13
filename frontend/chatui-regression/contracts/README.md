# ChatUI regression contracts

This directory contains the deterministic, browser-driven ChatUI regression
contracts. Run them through the repository runner so every execution records a
trace, video, screenshots, test output, and an aggregate result:

```bash
npm run qa:chatui
```

Use `--capture` while establishing a red baseline. It returns zero for completed,
evidenced product failures; typecheck, startup, and evidence failures still exit
non-zero:

```bash
npm run qa:chatui -- --capture
```

The suite is intentionally opt-in and is not part of the regular CI shard.
