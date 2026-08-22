data "aws_availability_zones" "available" {
  state = "available"
}

data "aws_caller_identity" "current" {}

locals {
  name               = "ao-cloud-${var.environment}"
  availability_zones = slice(data.aws_availability_zones.available.names, 0, 2)
  secret_prefix      = "ao-cloud/${var.environment}"
}

resource "aws_vpc" "this" {
  cidr_block           = var.vpc_cidr
  enable_dns_hostnames = true
  enable_dns_support   = true

  tags = { Name = local.name }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = local.name }
}

resource "aws_subnet" "public" {
  count                   = 2
  vpc_id                  = aws_vpc.this.id
  availability_zone       = local.availability_zones[count.index]
  cidr_block              = cidrsubnet(var.vpc_cidr, 4, count.index)
  map_public_ip_on_launch = true

  tags = { Name = "${local.name}-public-${count.index + 1}" }
}

resource "aws_subnet" "database" {
  count             = 2
  vpc_id            = aws_vpc.this.id
  availability_zone = local.availability_zones[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 4, count.index + 8)

  tags = { Name = "${local.name}-database-${count.index + 1}" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }

  tags = { Name = "${local.name}-public" }
}

resource "aws_route_table_association" "public" {
  count          = 2
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table" "database" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = "${local.name}-database" }
}

resource "aws_route_table_association" "database" {
  count          = 2
  subnet_id      = aws_subnet.database[count.index].id
  route_table_id = aws_route_table.database.id
}

resource "aws_security_group" "vpc_link" {
  name        = "${local.name}-vpc-link"
  description = "API Gateway VPC link"
  vpc_id      = aws_vpc.this.id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "load_balancer" {
  name        = "${local.name}-alb"
  description = "Internal control-plane load balancer"
  vpc_id      = aws_vpc.this.id

  ingress {
    from_port       = 80
    to_port         = 80
    protocol        = "tcp"
    security_groups = [aws_security_group.vpc_link.id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "ecs" {
  name        = "${local.name}-ecs"
  description = "Control-plane Fargate tasks"
  vpc_id      = aws_vpc.this.id

  ingress {
    from_port       = 8080
    to_port         = 8080
    protocol        = "tcp"
    security_groups = [aws_security_group.load_balancer.id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "database" {
  name        = "${local.name}-database"
  description = "PostgreSQL from control-plane tasks only"
  vpc_id      = aws_vpc.this.id

  ingress {
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.ecs.id]
  }
}

resource "aws_kms_key" "cloud" {
  description             = "AO Cloud ${var.environment} data and secrets"
  deletion_window_in_days = 30
  enable_key_rotation     = true
}

resource "aws_kms_alias" "cloud" {
  name          = "alias/${local.name}"
  target_key_id = aws_kms_key.cloud.key_id
}

resource "aws_db_subnet_group" "this" {
  name       = local.name
  subnet_ids = aws_subnet.database[*].id
}

resource "aws_db_instance" "this" {
  identifier                    = "${local.name}-postgres"
  engine                        = "postgres"
  engine_version                = "16"
  instance_class                = var.database_instance_class
  allocated_storage             = 20
  max_allocated_storage         = 100
  storage_type                  = "gp3"
  storage_encrypted             = true
  kms_key_id                    = aws_kms_key.cloud.arn
  db_name                       = var.database_name
  username                      = var.database_owner_user
  manage_master_user_password   = true
  master_user_secret_kms_key_id = aws_kms_key.cloud.arn
  db_subnet_group_name          = aws_db_subnet_group.this.name
  vpc_security_group_ids        = [aws_security_group.database.id]
  publicly_accessible           = false
  multi_az                      = false
  backup_retention_period       = 1
  deletion_protection           = true
  skip_final_snapshot           = true
  auto_minor_version_upgrade    = true
  apply_immediately             = true
}

resource "aws_secretsmanager_secret" "database" {
  name                    = "${local.secret_prefix}/database"
  description             = "Runtime and migration URLs populated by deploy-staging.sh"
  kms_key_id              = aws_kms_key.cloud.arn
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret" "application" {
  name                    = "${local.secret_prefix}/application"
  description             = "AO token signing key and accepted Google client IDs"
  kms_key_id              = aws_kms_key.cloud.arn
  recovery_window_in_days = 7
}

resource "aws_ecr_repository" "control_plane" {
  name                 = "ao-cloud-control-plane"
  image_tag_mutability = "IMMUTABLE"

  encryption_configuration {
    encryption_type = "KMS"
    kms_key         = aws_kms_key.cloud.arn
  }

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "control_plane" {
  repository = aws_ecr_repository.control_plane.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the newest 20 images"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 20
      }
      action = { type = "expire" }
    }]
  })
}

resource "aws_cloudwatch_log_group" "control_plane" {
  name              = "/ao-cloud/${var.environment}/control-plane"
  retention_in_days = 30
}

resource "aws_cloudwatch_log_group" "api_gateway" {
  name              = "/ao-cloud/${var.environment}/api-gateway"
  retention_in_days = 30
}

data "aws_iam_policy_document" "ecs_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ecs_execution" {
  name               = "${local.name}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

resource "aws_iam_role_policy_attachment" "ecs_execution" {
  role       = aws_iam_role.ecs_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "ecs_secrets" {
  statement {
    actions = ["secretsmanager:GetSecretValue"]
    resources = [
      aws_secretsmanager_secret.database.arn,
      aws_secretsmanager_secret.application.arn,
      aws_db_instance.this.master_user_secret[0].secret_arn,
    ]
  }

  statement {
    actions   = ["kms:Decrypt"]
    resources = [aws_kms_key.cloud.arn]
  }
}

resource "aws_iam_role_policy" "ecs_secrets" {
  name   = "secrets"
  role   = aws_iam_role.ecs_execution.id
  policy = data.aws_iam_policy_document.ecs_secrets.json
}

resource "aws_iam_role" "ecs_task" {
  name               = "${local.name}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

resource "aws_ecs_cluster" "this" {
  name = local.name

  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

locals {
  log_configuration = {
    logDriver = "awslogs"
    options = {
      awslogs-group         = aws_cloudwatch_log_group.control_plane.name
      awslogs-region        = var.aws_region
      awslogs-stream-prefix = "service"
    }
  }
}

resource "aws_ecs_task_definition" "api" {
  family                   = "${local.name}-api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"
  execution_role_arn       = aws_iam_role.ecs_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  container_definitions = jsonencode([{
    name      = "control-plane"
    image     = var.control_plane_image
    essential = true
    environment = [
      { name = "AO_CLOUD_ADDR", value = "0.0.0.0:8080" },
      { name = "AO_CLOUD_ACCESS_TOKEN_ISSUER", value = "ao-cloud-${var.environment}" },
      { name = "AO_CLOUD_ACCESS_TOKEN_AUDIENCE", value = "ao-desktop" },
      { name = "AO_CLOUD_PUBLIC_URL", value = trimsuffix(aws_apigatewayv2_stage.default.invoke_url, "/") },
    ]
    secrets = [
      { name = "AO_CLOUD_DATABASE_URL", valueFrom = "${aws_secretsmanager_secret.database.arn}:runtimeUrl::" },
      { name = "AO_CLOUD_GOOGLE_CLIENT_IDS", valueFrom = "${aws_secretsmanager_secret.application.arn}:googleClientIds::" },
      { name = "AO_CLOUD_ALLOWED_EMAILS", valueFrom = "${aws_secretsmanager_secret.application.arn}:allowedEmails::" },
      { name = "AO_CLOUD_ACCESS_TOKEN_KEY_BASE64", valueFrom = "${aws_secretsmanager_secret.application.arn}:accessTokenKeyBase64::" },
      { name = "DAYTONA_API_KEY", valueFrom = "${aws_secretsmanager_secret.application.arn}:daytonaApiKey::" },
      { name = "DAYTONA_API_URL", valueFrom = "${aws_secretsmanager_secret.application.arn}:daytonaApiUrl::" },
      { name = "DAYTONA_TARGET", valueFrom = "${aws_secretsmanager_secret.application.arn}:daytonaTarget::" },
      { name = "AO_CLOUD_GITHUB_TOKEN_BASE64", valueFrom = "${aws_secretsmanager_secret.application.arn}:githubTokenBase64::" },
    ]
    portMappings     = [{ containerPort = 8080, hostPort = 8080, protocol = "tcp" }]
    logConfiguration = local.log_configuration
  }])
}

resource "aws_ecs_task_definition" "migration" {
  family                   = "${local.name}-migrate"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"
  execution_role_arn       = aws_iam_role.ecs_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  container_definitions = jsonencode([{
    name       = "migration"
    image      = var.control_plane_image
    essential  = true
    entryPoint = ["/ao-cloud-migrate"]
    environment = [
      { name = "AO_CLOUD_RUNTIME_DATABASE_ROLE", value = var.database_runtime_user },
    ]
    secrets = [
      { name = "AO_CLOUD_MIGRATION_DATABASE_URL", valueFrom = "${aws_secretsmanager_secret.database.arn}:migrationUrl::" },
      { name = "AO_CLOUD_RUNTIME_DATABASE_PASSWORD", valueFrom = "${aws_secretsmanager_secret.database.arn}:runtimePassword::" },
    ]
    logConfiguration = merge(local.log_configuration, {
      options = merge(local.log_configuration.options, { awslogs-stream-prefix = "migration" })
    })
  }])
}

resource "aws_lb" "this" {
  name               = substr(local.name, 0, 32)
  internal           = true
  load_balancer_type = "application"
  security_groups    = [aws_security_group.load_balancer.id]
  subnets            = aws_subnet.public[*].id
}

resource "aws_lb_target_group" "api" {
  name                 = substr("${local.name}-api", 0, 32)
  port                 = 8080
  protocol             = "HTTP"
  target_type          = "ip"
  vpc_id               = aws_vpc.this.id
  deregistration_delay = 15

  health_check {
    path                = "/readyz"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    interval            = 15
    timeout             = 5
    matcher             = "200"
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }
}

resource "aws_ecs_service" "api" {
  name                              = "${local.name}-api"
  cluster                           = aws_ecs_cluster.this.id
  task_definition                   = aws_ecs_task_definition.api.arn
  desired_count                     = var.deployment_enabled ? 1 : 0
  launch_type                       = "FARGATE"
  platform_version                  = "LATEST"
  health_check_grace_period_seconds = 90
  wait_for_steady_state             = true

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.ecs.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.api.arn
    container_name   = "control-plane"
    container_port   = 8080
  }

  depends_on = [aws_lb_listener.http]
}

resource "aws_apigatewayv2_api" "this" {
  name          = local.name
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_vpc_link" "this" {
  name               = local.name
  security_group_ids = [aws_security_group.vpc_link.id]
  subnet_ids         = aws_subnet.public[*].id
}

resource "aws_apigatewayv2_integration" "alb" {
  api_id                 = aws_apigatewayv2_api.this.id
  integration_type       = "HTTP_PROXY"
  integration_method     = "ANY"
  integration_uri        = aws_lb_listener.http.arn
  connection_type        = "VPC_LINK"
  connection_id          = aws_apigatewayv2_vpc_link.this.id
  payload_format_version = "1.0"
  timeout_milliseconds   = 30000
}

resource "aws_apigatewayv2_route" "default" {
  api_id    = aws_apigatewayv2_api.this.id
  route_key = "$default"
  target    = "integrations/${aws_apigatewayv2_integration.alb.id}"
}

resource "aws_apigatewayv2_stage" "default" {
  api_id      = aws_apigatewayv2_api.this.id
  name        = "$default"
  auto_deploy = true

  access_log_settings {
    destination_arn = aws_cloudwatch_log_group.api_gateway.arn
    format = jsonencode({
      requestId      = "$context.requestId"
      requestTime    = "$context.requestTime"
      httpMethod     = "$context.httpMethod"
      routeKey       = "$context.routeKey"
      status         = "$context.status"
      responseLength = "$context.responseLength"
    })
  }
}

data "aws_iam_policy_document" "codebuild_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["codebuild.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "codebuild" {
  name               = "${local.name}-codebuild"
  assume_role_policy = data.aws_iam_policy_document.codebuild_assume.json
}

data "aws_iam_policy_document" "codebuild" {
  statement {
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:CompleteLayerUpload",
      "ecr:GetDownloadUrlForLayer",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
    ]
    resources = [aws_ecr_repository.control_plane.arn]
  }

  statement {
    actions = [
      "logs:CreateLogGroup",
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/codebuild/${local.name}*"]
  }

  statement {
    actions   = ["kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey"]
    resources = [aws_kms_key.cloud.arn]
  }
}

resource "aws_iam_role_policy" "codebuild" {
  name   = "build-and-push"
  role   = aws_iam_role.codebuild.id
  policy = data.aws_iam_policy_document.codebuild.json
}

resource "aws_codebuild_project" "control_plane" {
  name          = local.name
  service_role  = aws_iam_role.codebuild.arn
  build_timeout = 30

  artifacts { type = "NO_ARTIFACTS" }
  source {
    type      = "NO_SOURCE"
    buildspec = <<-YAML
      version: 0.2
      phases:
        pre_build:
          commands:
            - REGISTRY="$${ECR_REPOSITORY_URL%%/*}"
            - aws ecr get-login-password --region "$${AWS_DEFAULT_REGION}" | docker login --username AWS --password-stdin "$${REGISTRY}"
            - git init source
            - git -C source fetch --depth=1 "$${SOURCE_REPOSITORY_URL}" "$${SOURCE_COMMIT}"
            - git -C source checkout --detach FETCH_HEAD
        build:
          commands:
            - docker build --file source/deploy/cloud/Dockerfile --tag "$${ECR_REPOSITORY_URL}:$${IMAGE_TAG}" source
        post_build:
          commands:
            - docker push "$${ECR_REPOSITORY_URL}:$${IMAGE_TAG}"
      YAML
  }

  environment {
    compute_type                = "BUILD_GENERAL1_SMALL"
    image                       = "aws/codebuild/standard:7.0"
    type                        = "LINUX_CONTAINER"
    privileged_mode             = true
    image_pull_credentials_type = "CODEBUILD"

    environment_variable {
      name  = "ECR_REPOSITORY_URL"
      value = aws_ecr_repository.control_plane.repository_url
    }
    environment_variable {
      name  = "SOURCE_REPOSITORY_URL"
      value = "https://github.com/Untrivial-ai/agent-orchestrator.git"
    }
  }
}
