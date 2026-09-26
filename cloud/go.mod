module github.com/aoagents/agent-orchestrator/cloud

go 1.27.1

require (
	github.com/aoagents/agent-orchestrator/backend v0.0.0
	github.com/coder/websocket v1.8.15
	github.com/coreos/go-oidc/v3 v3.20.0
	github.com/creack/pty v1.1.24
	github.com/go-chi/chi/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/pressly/goose/v3 v3.27.3
	golang.org/x/crypto v0.54.0
)

require (
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/sethvargo/go-retry v0.4.0 // indirect
	github.com/spf13/cobra v1.10.1 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace github.com/aoagents/agent-orchestrator/backend => github.com/Untrivial-ai/agent-orchestrator/backend v0.0.0-20260925061453-06fc5e595c32
