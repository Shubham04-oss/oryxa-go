#!/usr/bin/env bash
# Creates the Vertex AI agent engine whose Memory Bank holds what Krude
# remembers across a member's threads, and writes its id to
# memory.auto.tfvars.json for the next `tofu apply`. Repeatable: an engine of
# the same display name is reused.
#
# Agent engines are regional (gcp_region, beside the AWS region), while the
# Gemini 3 models are served from the global endpoint only, so memories are
# extracted by the global model and embedded by one in the engine's region.
# MEMORY_GENERATION_MODEL and MEMORY_EMBEDDING_MODEL override either, as full
# publisher model resource names.
#
# It calls Vertex AI as the operator, with Application Default Credentials
# (`gcloud auth application-default login`).

source "$(dirname "$0")/lib.sh"

project=$(tofu -chdir="$TF" output -raw gcp_project)
region=$(tofu -chdir="$TF" output -raw gcp_region)
model=$(tofu -chdir="$TF" output -raw model)
name="$(tf_output name)-memory"
generation=${MEMORY_GENERATION_MODEL:-projects/$project/locations/global/publishers/google/models/$model}
embedding=${MEMORY_EMBEDDING_MODEL:-projects/$project/locations/$region/publishers/google/models/text-embedding-005}

api="https://$region-aiplatform.googleapis.com/v1beta1"
token=$(gcloud auth application-default print-access-token)
call() {
  curl -fsS -H "Authorization: Bearer $token" -H "x-goog-user-project: $project" \
    -H "Content-Type: application/json" "$@"
}

engine=$(call "$api/projects/$project/locations/$region/reasoningEngines" |
  jq -r --arg name "$name" '[.reasoningEngines[]? | select(.displayName == $name)][0].name // empty')

if [[ -z $engine ]]; then
  step "creating $name in $region"
  operation=$(call -X POST "$api/projects/$project/locations/$region/reasoningEngines" -d "$(jq -n \
    --arg name "$name" --arg generation "$generation" --arg embedding "$embedding" '{
      displayName: $name,
      description: "What Krude remembers across a member'"'"'s threads",
      contextSpec: {memoryBankConfig: {
        generationConfig: {model: $generation},
        similaritySearchConfig: {embeddingModel: $embedding}
      }}
    }')")
  operation_name=$(jq -r .name <<<"$operation")
  until [[ "$(jq -r '.done // false' <<<"$operation")" == true ]]; do
    sleep 5
    operation=$(call "https://$region-aiplatform.googleapis.com/v1beta1/$operation_name")
  done
  if jq -e .error <<<"$operation" >/dev/null; then
    jq .error <<<"$operation" >&2
    exit 1
  fi
  engine=$(jq -r .response.name <<<"$operation")
fi

id=${engine##*/}
jq -n --arg id "$id" '{memory_agent_engine: $id}' >"$TF/memory.auto.tfvars.json"
step "memory bank: $engine"
