output "api_url" {
  value = aws_apigatewayv2_stage.default.invoke_url
}

output "ecr_repository_url" {
  value = aws_ecr_repository.control_plane.repository_url
}

output "codebuild_project" {
  value = aws_codebuild_project.control_plane.name
}

output "ecs_cluster" {
  value = aws_ecs_cluster.this.name
}

output "ecs_service" {
  value = aws_ecs_service.api.name
}

output "migration_task_definition_arn" {
  value = aws_ecs_task_definition.migration.arn
}

output "public_subnet_ids" {
  value = aws_subnet.public[*].id
}

output "ecs_security_group_id" {
  value = aws_security_group.ecs.id
}

output "database_secret_arn" {
  value = aws_secretsmanager_secret.database.arn
}

output "application_secret_arn" {
  value = aws_secretsmanager_secret.application.arn
}

output "rds_master_secret_arn" {
  value     = aws_db_instance.this.master_user_secret[0].secret_arn
  sensitive = true
}

output "database_address" {
  value = aws_db_instance.this.address
}

output "database_name" {
  value = var.database_name
}

output "database_runtime_user" {
  value = var.database_runtime_user
}
