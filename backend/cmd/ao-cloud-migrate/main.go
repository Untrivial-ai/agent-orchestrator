// Command ao-cloud-migrate applies hosted control-plane PostgreSQL migrations.
package main

import (
	"context"
	"log"
	"os"
	"strings"

	cloudpostgres "github.com/aoagents/agent-orchestrator/backend/internal/cloud/postgres"
)

func main() {
	databaseURL := strings.TrimSpace(os.Getenv("AO_CLOUD_MIGRATION_DATABASE_URL"))
	runtimeRole := strings.TrimSpace(os.Getenv("AO_CLOUD_RUNTIME_DATABASE_ROLE"))
	if err := cloudpostgres.Migrate(context.Background(), databaseURL, runtimeRole); err != nil {
		log.Fatal(err)
	}
}
