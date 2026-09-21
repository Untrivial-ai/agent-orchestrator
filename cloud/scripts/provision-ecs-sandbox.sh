#!/usr/bin/env bash
set -euo pipefail

# One-time (idempotent) provisioner for the ECS-on-EC2 Graviton sandbox provider.
#
# It creates, and re-running only fills in what is missing:
#   - an ECS cluster dedicated to sandbox tasks
#   - an EC2 launch template on Graviton (arm64 ECS-optimized AL2023 AMI)
#   - an Auto Scaling group across the given subnets
#   - an ECS managed-scaling capacity provider bound to that ASG and cluster
#   - a security group for the instances (egress only; no ingress)
#   - the IAM roles: EC2 container-instance role + instance profile, the task
#     execution role, and an (empty) sandbox task role
#   - a RunTask policy attached to the control plane's task role so the CP can
#     run, stop, describe, list and tag sandbox tasks
#   - the sandbox task definition (bridge networking, arm64, container "worker")
#
# It does NOT build or push the worker image; run build-ecs-sandbox-image.sh for
# that (it registers the task-definition revision that carries the image). The
# first run here registers a bootstrap revision pointing at :latest so the family
# exists for the build script to update.
#
# Required inputs (no safe default; pass as environment variables):
#   AO_CLOUD_ECS_SUBNETS         comma-separated private subnet ids for the ASG
#   AO_CLOUD_ECS_VPC_ID          the VPC those subnets live in
#   AO_CLOUD_CP_TASK_ROLE_NAME   the control plane's ECS task role (name, not ARN)
#
# Optional inputs (shown with their defaults):
REGION="${AWS_REGION:-eu-north-1}"
CLUSTER="${AO_CLOUD_ECS_CLUSTER:-ao-cloud-staging-sandboxes}"
CAPACITY_PROVIDER="${AO_CLOUD_ECS_CAPACITY_PROVIDER:-ao-cloud-sandbox-graviton}"
ASG_NAME="${AO_CLOUD_ECS_ASG_NAME:-ao-cloud-sandbox-graviton}"
LAUNCH_TEMPLATE="${AO_CLOUD_ECS_LAUNCH_TEMPLATE:-ao-cloud-sandbox-graviton}"
INSTANCE_TYPE="${AO_CLOUD_ECS_INSTANCE_TYPE:-r7g.xlarge}"
ASG_MIN="${AO_CLOUD_ECS_ASG_MIN:-0}"
ASG_MAX="${AO_CLOUD_ECS_ASG_MAX:-10}"
ASG_DESIRED="${AO_CLOUD_ECS_ASG_DESIRED:-1}"
TARGET_CAPACITY="${AO_CLOUD_ECS_TARGET_CAPACITY:-100}"
TASK_FAMILY="${AO_CLOUD_ECS_TASK_FAMILY:-ao-cloud-sandbox-worker}"
CONTAINER_NAME="${AO_CLOUD_ECS_CONTAINER_NAME:-worker}"
NAMESPACE="${AO_CLOUD_ECS_NAMESPACE:-ao-cloud}"
WORKER_REPOSITORY="${AO_CLOUD_WORKER_ECR_REPOSITORY:-ao-cloud-worker}"
LOG_GROUP="${AO_CLOUD_ECS_LOG_GROUP:-/ao-cloud/staging/sandbox}"
# Task sizing (EC2 launch type: these are container reservations, not Fargate
# sizes). 2 vCPU / 4 GiB matches the nodeops s-2vcpu-4gb profile and packs ~3
# sandboxes onto a c7g.2xlarge (8 vCPU / ~15.6 GiB) instead of one per box.
TASK_CPU="${AO_CLOUD_ECS_TASK_CPU:-2048}"
TASK_MEMORY="${AO_CLOUD_ECS_TASK_MEMORY:-4096}"

: "${AO_CLOUD_ECS_SUBNETS:?set AO_CLOUD_ECS_SUBNETS to comma-separated private subnet ids}"
: "${AO_CLOUD_ECS_VPC_ID:?set AO_CLOUD_ECS_VPC_ID to the VPC of those subnets}"
: "${AO_CLOUD_CP_TASK_ROLE_NAME:?set AO_CLOUD_CP_TASK_ROLE_NAME to the control plane task role name}"

AWS_OPTIONS=(--region "$REGION")
if [[ -n "${AWS_PROFILE:-}" ]]; then
	AWS_OPTIONS+=(--profile "$AWS_PROFILE")
fi
aws_cli() { aws "${AWS_OPTIONS[@]}" "$@"; }

account_id="$(aws_cli sts get-caller-identity --query Account --output text)"
echo "Provisioning ECS sandbox infra in ${REGION} (account ${account_id})"

# --- IAM: EC2 container-instance role + instance profile ---------------------
instance_role="ao-cloud-sandbox-instance"
if ! aws_cli iam get-role --role-name "$instance_role" >/dev/null 2>&1; then
	aws_cli iam create-role --role-name "$instance_role" \
		--assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null
fi
aws_cli iam attach-role-policy --role-name "$instance_role" \
	--policy-arn arn:aws:iam::aws:policy/service-role/AmazonEC2ContainerServiceforEC2Role >/dev/null || true
aws_cli iam attach-role-policy --role-name "$instance_role" \
	--policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore >/dev/null || true
if ! aws_cli iam get-instance-profile --instance-profile-name "$instance_role" >/dev/null 2>&1; then
	aws_cli iam create-instance-profile --instance-profile-name "$instance_role" >/dev/null
	aws_cli iam add-role-to-instance-profile --instance-profile-name "$instance_role" \
		--role-name "$instance_role" >/dev/null
fi

# --- IAM: task execution role (pull image, write logs) -----------------------
execution_role="ao-cloud-sandbox-execution"
if ! aws_cli iam get-role --role-name "$execution_role" >/dev/null 2>&1; then
	aws_cli iam create-role --role-name "$execution_role" \
		--assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null
fi
aws_cli iam attach-role-policy --role-name "$execution_role" \
	--policy-arn arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy >/dev/null || true

# --- IAM: sandbox task role (the worker needs no AWS permissions) -------------
task_role="ao-cloud-sandbox-task"
if ! aws_cli iam get-role --role-name "$task_role" >/dev/null 2>&1; then
	aws_cli iam create-role --role-name "$task_role" \
		--assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null
fi
execution_role_arn="arn:aws:iam::${account_id}:role/${execution_role}"
task_role_arn="arn:aws:iam::${account_id}:role/${task_role}"

# --- IAM: let the control plane run sandbox tasks ----------------------------
# ecs:RunTask/StopTask/DescribeTasks/ListTasks scoped to the sandbox cluster,
# plus iam:PassRole for exactly the two sandbox roles and ecs:TagResource.
cp_policy_doc="$(cat <<JSON
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "SandboxRunLifecycle",
      "Effect": "Allow",
      "Action": ["ecs:RunTask", "ecs:StopTask", "ecs:DescribeTasks", "ecs:ListTasks", "ecs:TagResource"],
      "Resource": "*",
      "Condition": {"ArnEquals": {"ecs:cluster": "arn:aws:ecs:${REGION}:${account_id}:cluster/${CLUSTER}"}}
    },
    {
      "Sid": "SandboxPassRoles",
      "Effect": "Allow",
      "Action": "iam:PassRole",
      "Resource": ["${execution_role_arn}", "${task_role_arn}"],
      "Condition": {"StringEquals": {"iam:PassedToService": "ecs-tasks.amazonaws.com"}}
    }
  ]
}
JSON
)"
aws_cli iam put-role-policy --role-name "$AO_CLOUD_CP_TASK_ROLE_NAME" \
	--policy-name ao-cloud-sandbox-runtask --policy-document "$cp_policy_doc" >/dev/null
echo "Attached RunTask policy to control-plane role ${AO_CLOUD_CP_TASK_ROLE_NAME}"

# --- CloudWatch log group ----------------------------------------------------
aws_cli logs create-log-group --log-group-name "$LOG_GROUP" >/dev/null 2>&1 || true

# --- ECS cluster -------------------------------------------------------------
aws_cli ecs create-cluster --cluster-name "$CLUSTER" \
	--tags key=Project,value=ao-cloud key=Component,value=sandbox >/dev/null 2>&1 || true

# --- Security group (egress only) --------------------------------------------
sg_id="$(
	aws_cli ec2 describe-security-groups \
		--filters "Name=group-name,Values=ao-cloud-sandbox" "Name=vpc-id,Values=${AO_CLOUD_ECS_VPC_ID}" \
		--query 'SecurityGroups[0].GroupId' --output text 2>/dev/null || true
)"
if [[ -z "$sg_id" || "$sg_id" == "None" ]]; then
	sg_id="$(
		aws_cli ec2 create-security-group \
			--group-name ao-cloud-sandbox \
			--description "AO Cloud sandbox instances (egress only)" \
			--vpc-id "$AO_CLOUD_ECS_VPC_ID" \
			--query GroupId --output text
	)"
	# A fresh SG already permits all egress; there is no ingress rule, so the
	# worker can dial the control plane, GitHub and npm but nothing can reach in.
fi
echo "Security group ${sg_id}"

# --- Launch template (Graviton, arm64 ECS-optimized AL2023) ------------------
# Prefer a baked AMI (stock ECS-optimized arm64 + the worker image pre-pulled) so
# new instances start sandbox containers in seconds with no image pull. Set
# AO_CLOUD_ECS_AMI_ID to that baked AMI; otherwise fall back to the latest stock
# ECS-optimized arm64 AMI (first boot on each instance then pays a one-time pull).
ami_id="${AO_CLOUD_ECS_AMI_ID:-}"
if [[ -z "${ami_id}" ]]; then
	ami_id="$(
		aws_cli ssm get-parameters \
			--names /aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id \
			--query 'Parameters[0].Value' --output text
	)"
	echo "arm64 ECS-optimized AMI (stock) ${ami_id}"
else
	echo "arm64 ECS-optimized AMI (baked, AO_CLOUD_ECS_AMI_ID) ${ami_id}"
fi
user_data_b64="$(printf '#!/bin/bash\necho ECS_CLUSTER=%s >> /etc/ecs/ecs.config\n' "$CLUSTER" | base64)"
lt_data="$(cat <<JSON
{
  "ImageId": "${ami_id}",
  "InstanceType": "${INSTANCE_TYPE}",
  "IamInstanceProfile": {"Name": "${instance_role}"},
  "SecurityGroupIds": ["${sg_id}"],
  "UserData": "${user_data_b64}",
  "TagSpecifications": [{"ResourceType": "instance", "Tags": [{"Key": "Name", "Value": "ao-cloud-sandbox"}, {"Key": "Project", "Value": "ao-cloud"}]}]
}
JSON
)"
if aws_cli ec2 describe-launch-templates --launch-template-names "$LAUNCH_TEMPLATE" >/dev/null 2>&1; then
	aws_cli ec2 create-launch-template-version \
		--launch-template-name "$LAUNCH_TEMPLATE" \
		--launch-template-data "$lt_data" \
		--default-version '$Latest' >/dev/null
else
	aws_cli ec2 create-launch-template \
		--launch-template-name "$LAUNCH_TEMPLATE" \
		--launch-template-data "$lt_data" >/dev/null
fi

# --- Auto Scaling group ------------------------------------------------------
subnet_list="${AO_CLOUD_ECS_SUBNETS}"
if aws_cli autoscaling describe-auto-scaling-groups --auto-scaling-group-names "$ASG_NAME" \
	--query 'AutoScalingGroups[0].AutoScalingGroupName' --output text 2>/dev/null | grep -q "$ASG_NAME"; then
	aws_cli autoscaling update-auto-scaling-group \
		--auto-scaling-group-name "$ASG_NAME" \
		--launch-template "LaunchTemplateName=${LAUNCH_TEMPLATE},Version=\$Latest" \
		--min-size "$ASG_MIN" --max-size "$ASG_MAX" --desired-capacity "$ASG_DESIRED" \
		--vpc-zone-identifier "$subnet_list" >/dev/null
else
	aws_cli autoscaling create-auto-scaling-group \
		--auto-scaling-group-name "$ASG_NAME" \
		--launch-template "LaunchTemplateName=${LAUNCH_TEMPLATE},Version=\$Latest" \
		--min-size "$ASG_MIN" --max-size "$ASG_MAX" --desired-capacity "$ASG_DESIRED" \
		--vpc-zone-identifier "$subnet_list" \
		--new-instances-protected-from-scale-in \
		--tags "ResourceId=${ASG_NAME},ResourceType=auto-scaling-group,Key=AmazonECSManaged,Value=true,PropagateAtLaunch=true" >/dev/null
fi
asg_arn="$(
	aws_cli autoscaling describe-auto-scaling-groups \
		--auto-scaling-group-names "$ASG_NAME" \
		--query 'AutoScalingGroups[0].AutoScalingGroupARN' --output text
)"
echo "Auto Scaling group ${asg_arn}"

# --- ECS capacity provider + cluster association -----------------------------
if ! aws_cli ecs describe-capacity-providers --capacity-providers "$CAPACITY_PROVIDER" \
	--query 'capacityProviders[0].name' --output text 2>/dev/null | grep -q "$CAPACITY_PROVIDER"; then
	aws_cli ecs create-capacity-provider \
		--name "$CAPACITY_PROVIDER" \
		--auto-scaling-group-provider "autoScalingGroupArn=${asg_arn},managedScaling={status=ENABLED,targetCapacity=${TARGET_CAPACITY},minimumScalingStepSize=1,maximumScalingStepSize=100},managedTerminationProtection=ENABLED" >/dev/null
fi
aws_cli ecs put-cluster-capacity-providers \
	--cluster "$CLUSTER" \
	--capacity-providers "$CAPACITY_PROVIDER" \
	--default-capacity-provider-strategy "capacityProvider=${CAPACITY_PROVIDER},weight=1" >/dev/null
echo "Capacity provider ${CAPACITY_PROVIDER} bound to ${CLUSTER}"

# --- Bootstrap sandbox task definition ---------------------------------------
# A first revision pointing at :latest so the family exists; build-ecs-sandbox-image.sh
# then registers the real revision with the pinned arm64 digest.
if ! aws_cli ecs describe-task-definition --task-definition "$TASK_FAMILY" >/dev/null 2>&1; then
	repository_uri="$(
		aws_cli ecr describe-repositories \
			--repository-names "$WORKER_REPOSITORY" \
			--query 'repositories[0].repositoryUri' --output text
	)"
	task_def="$(cat <<JSON
{
  "family": "${TASK_FAMILY}",
  "networkMode": "bridge",
  "requiresCompatibilities": ["EC2"],
  "cpu": "${TASK_CPU}",
  "memory": "${TASK_MEMORY}",
  "runtimePlatform": {"cpuArchitecture": "ARM64", "operatingSystemFamily": "LINUX"},
  "executionRoleArn": "${execution_role_arn}",
  "taskRoleArn": "${task_role_arn}",
  "containerDefinitions": [
    {
      "name": "${CONTAINER_NAME}",
      "image": "${repository_uri}:latest",
      "cpu": ${TASK_CPU},
      "memory": ${TASK_MEMORY},
      "essential": true,
      "logConfiguration": {
        "logDriver": "awslogs",
        "options": {
          "awslogs-group": "${LOG_GROUP}",
          "awslogs-region": "${REGION}",
          "awslogs-stream-prefix": "sandbox"
        }
      }
    }
  ]
}
JSON
)"
	aws_cli ecs register-task-definition --cli-input-json "$task_def" \
		--query 'taskDefinition.taskDefinitionArn' --output text
fi

cat <<SUMMARY

ECS sandbox infra ready. Set these on the control plane task (deploy-staging.sh
does this for you when AO_CLOUD_ECS_ENABLED=1):

  AO_CLOUD_SANDBOX_PROVIDERS=coder,ecs
  AO_CLOUD_ECS_REGION=${REGION}
  AO_CLOUD_ECS_CLUSTER=${CLUSTER}
  AO_CLOUD_ECS_TASK_DEFINITION=${TASK_FAMILY}
  AO_CLOUD_ECS_CONTAINER_NAME=${CONTAINER_NAME}
  AO_CLOUD_ECS_CAPACITY_PROVIDER=${CAPACITY_PROVIDER}
  AO_CLOUD_ECS_NAMESPACE=${NAMESPACE}

Next: build and register the arm64 worker image with
  AWS_PROFILE=${AWS_PROFILE:-ao-cloud} ./scripts/build-ecs-sandbox-image.sh
SUMMARY
