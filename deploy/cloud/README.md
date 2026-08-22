# AWS staging deployment

This directory deploys the PostgreSQL/auth/Daytona POC control plane to a
cost-oriented staging environment in `us-west-2`.

## Architecture

- API Gateway provides the public AWS-managed HTTPS endpoint.
- A VPC link reaches an internal Application Load Balancer.
- One ECS Fargate task runs `ao-cloud` in public subnets with outbound internet
  access for Google OIDC discovery. Its security group accepts traffic only
  from the internal load balancer.
- A single-AZ encrypted RDS PostgreSQL 16 instance stays in isolated database
  subnets and accepts traffic only from the ECS task security group.
- RDS manages and rotates the migration-owner secret. Separate Secrets Manager
  records hold the restricted runtime URL and AO signing key. Terraform creates
  secret containers but never receives their values.
- CodeBuild builds the exact Git commit and pushes an immutable image to ECR.
- CloudWatch receives API, migration, and API Gateway logs.

The task calls Daytona for a project coordinator and for a separate sandbox for
every orchestrator and worker session. The coordinator holds only a scoped AO
runtime capability; Daytona credentials remain in the control plane. This is
intentionally a staging stack. It excludes production multi-AZ RDS,
autoscaling, a custom domain, WAF, and durable lifecycle reconciliation.

## Deploy

Prerequisites are AWS CLI, Terraform 1.10+, `jq`, `openssl`, and credentials
authorized to create the resources above. Set the public Google desktop OAuth
client ID, then run:

```bash
export AO_CLOUD_GOOGLE_CLIENT_IDS='123456789-example.apps.googleusercontent.com'
export AO_CLOUD_ALLOWED_EMAILS='maintainer@example.com'
export DAYTONA_API_KEY='...'
export AO_CLOUD_GITHUB_TOKEN_BASE64="$(gh auth token | base64 | tr -d '\n')"
deploy/cloud/deploy-staging.sh
```

These values are written to AWS Secrets Manager and injected into ECS; they are
not Terraform variables, image layers, logs, or committed files.

Claude credentials are not a deployment secret. When a signed-in desktop
creates a cloud project, Electron main reads that user's existing Claude Code
credential from OS-protected storage and sends it over the authenticated TLS
request directly to the provisioner. The control plane holds it only in memory
while bootstrapping the user's sandbox and does not persist or log it.

`AO_CLOUD_ALLOWED_EMAILS` is a required, comma-separated staging signup gate.
Because this POC still injects a shared operator GitHub credential into
permitted sandboxes, use a fine-grained token limited to the test repositories
and set the shortest practical expiration. Rotate the shared token after each
test period; do not broaden the email allowlist until per-user GitHub credential
custody is implemented.

The script:

1. creates an encrypted, versioned, public-blocked S3 state bucket;
2. provisions networking, RDS, secrets, ECR, CodeBuild, ECS, API Gateway, and
   logging;
3. generates secrets outside Terraform state;
4. builds and pushes the exact current commit in CodeBuild;
5. runs the migration task before enabling the API service; and
6. forces a fresh ECS rollout so updated secret values are loaded; and
7. verifies health, readiness, invalid Google identity, and malformed refresh
   and logout behavior through the public HTTPS endpoint.

The script intentionally scales staging to zero during migration. Production
must use a separately reviewed expand/migrate/contract rollout.
