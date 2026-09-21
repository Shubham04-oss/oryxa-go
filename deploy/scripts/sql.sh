#!/usr/bin/env bash
# Runs SQL against the production database from a one-off Fargate task, for
# the rare repair no API makes. The database sits in private subnets and its
# password never leaves Secrets Manager, so the task borrows the service's own
# network, execution role and DATABASE_URL secret, prints what psql printed,
# and exits with psql's status. Everything in one call runs in one
# transaction.
#
#   deploy/scripts/sql.sh "SELECT status, count(*) FROM rag_documents GROUP BY 1"
#   deploy/scripts/sql.sh < repair.sql

source "$(dirname "$0")/lib.sh"

sql=${1:-$(cat)}
if [[ -z "${sql//[[:space:]]/}" ]]; then
  echo "usage: $0 'SQL' (or the SQL on stdin)" >&2
  exit 2
fi

cluster=$(tf_output cluster)
service=$(tf_output service)
family="$(tf_output name)-sql"
region=$(aws configure get region || true)
region=${AWS_REGION:-${region:-us-west-2}}

service_json=$(aws ecs describe-services --cluster "$cluster" --services "$service" --query 'services[0]')
network=$(jq -c '.networkConfiguration' <<<"$service_json")
task_json=$(aws ecs describe-task-definition --task-definition "$(jq -r '.taskDefinition' <<<"$service_json")" --query 'taskDefinition')
execution_role=$(jq -r '.executionRoleArn' <<<"$task_json")
database_url=$(jq -r '.containerDefinitions[] | select(.name == "oryxa") | .secrets[] | select(.name == "DATABASE_URL") | .valueFrom' <<<"$task_json")
log_group=$(jq -r '.containerDefinitions[] | select(.name == "oryxa") | .logConfiguration.options["awslogs-group"]' <<<"$task_json")

definition=$(jq -n --arg family "$family" --arg role "$execution_role" --arg url "$database_url" \
  --arg group "$log_group" --arg region "$region" --arg sql "$sql" '{
  family: $family,
  requiresCompatibilities: ["FARGATE"],
  networkMode: "awsvpc",
  cpu: "256",
  memory: "512",
  runtimePlatform: {cpuArchitecture: "ARM64", operatingSystemFamily: "LINUX"},
  executionRoleArn: $role,
  containerDefinitions: [{
    name: "psql",
    image: "public.ecr.aws/docker/library/postgres:17-alpine",
    essential: true,
    entryPoint: ["sh", "-c"],
    command: ["psql \"$DATABASE_URL\" --no-psqlrc -v ON_ERROR_STOP=1 -c \"$SQL\""],
    environment: [{name: "SQL", value: $sql}],
    secrets: [{name: "DATABASE_URL", valueFrom: $url}],
    logConfiguration: {logDriver: "awslogs", options: {
      "awslogs-group": $group, "awslogs-region": $region, "awslogs-stream-prefix": "sql"
    }}
  }]
}')
revision=$(aws ecs register-task-definition --cli-input-json "$definition" --query 'taskDefinition.taskDefinitionArn' --output text)
trap 'aws ecs deregister-task-definition --task-definition "$revision" >/dev/null' EXIT

step "running the SQL in a one-off task"
task=$(aws ecs run-task --cluster "$cluster" --launch-type FARGATE --task-definition "$revision" \
  --network-configuration "$network" --query 'tasks[0].taskArn' --output text)
aws ecs wait tasks-stopped --cluster "$cluster" --tasks "$task"

stopped=$(aws ecs describe-tasks --cluster "$cluster" --tasks "$task" --query 'tasks[0]')
aws logs get-log-events --log-group-name "$log_group" --log-stream-name "sql/psql/${task##*/}" \
  --start-from-head --query 'events[].message' --output text 2>/dev/null | tr '\t' '\n' || true
status=$(jq -r '.containers[0].exitCode // empty' <<<"$stopped")
if [[ "$status" != "0" ]]; then
  echo "the task stopped without success: $(jq -r '.stoppedReason' <<<"$stopped") (exit ${status:-none})" >&2
  exit 1
fi
