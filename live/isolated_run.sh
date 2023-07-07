#!/bin/bash

# This script allows you to run multiple terraform commands on multiple branches or change sets at the same time
# You will no longer need to wait for one run to finish before you can test the next change, nor will you have to juggle
# changesets or stashes when you want to deploy a different branch for testing.

# Check for the existence of required commands
for cmd in git docker rsync; do
  if ! command -v $cmd >/dev/null 2>&1; then
    echo "Error: $cmd is not installed or not in the PATH"
    exit 1
  fi
done

# Check if the Docker client can connect to the Docker host
if ! docker info >/dev/null 2>&1; then
  echo "Error: Docker client cannot connect to the Docker host"
  exit 1
fi

# Commands to run on script exit to ensure cleanup.
cleanup() {
  # Wait for the container to complete
  docker wait "$container_id"

  docker rm "$container_id"

  git worktree remove "${WORKTREE_DIR}" --force

  rm -fr "${WORKTREE_DIR}"

  echo "Cleanup complete."
}

# Default values
AWS_REGION=""
ENV=""
TERRAGRUNT_COMMAND=""
BRANCH_NAME=""
COPY_LIVE=true
CLEAR_TF=false
AWS_PROFILE="${TG_AWS_PROFILE}"
ADDL_ENV=""


# Define a function to display the usage
usage() {
  echo "Usage (defaults in stars (*)): ./isolated-run.sh (-r|--region) <region> (-e|--env) <env> (-c|--command) <terragrunt-command> [-b|--branch <branchname>] [-l|--copy_live <*true*|false>] [-f|--clear_tf <true|*false*>]"
}

# Parse command-line arguments using shift for both short and long options
while [[ "$#" -gt 0 ]]; do
  case $1 in
    -r|--region)
      AWS_REGION="${2:-${TG_AWS_REGION:-}}"
      shift
      ;;
    -e|--env)
      ENV="${2:-}"
      shift
      ;;
    -c|--command)
      TERRAGRUNT_COMMAND="${2:-}"
      shift
      ;;
    -b|--branch)
      BRANCH_NAME="${2:-}"
      shift
      ;;
    -l|--copy_live)
      COPY_LIVE="${2:-}"
      shift
      ;;
    -f|--clear_tf)
      CLEAR_TF="${2:-}"
      shift
      ;;
    *)
      usage
      exit 1
      ;;
  esac
  shift
done

# Check if the required arguments are provided
if [ -z "$AWS_REGION" ] || [ -z "$ENV" ] || [ -z "$TERRAGRUNT_COMMAND" ]; then
  usage
  exit 1
fi
if [ -n "$TG_AWS_ACCT_ID" ]; then
  ADDL_ENV="-e TG_AWS_ACCT_ID=$TG_AWS_ACCT_ID"
fi;

# Capture script error or exit command and run the cleanup function to ensure resources are deleted in a failure.
trap cleanup EXIT

TIMESTAMP=$(date "+%Y-%m-%d_%H-%M-%S")

# Replace spaces in the command with dashes so its file/directory safe.
COMMAND_SUFFIX="${TERRAGRUNT_COMMAND// /-}"

TEMP_DIR=$(mktemp -d)

WORKTREE_DIR="${TEMP_DIR}/${TIMESTAMP}/worktree"

# If no branch is provided, we assume you want a copy of the currently checked out branch.
if [ -z "$BRANCH_NAME" ]; then
  BRANCH_NAME=$(git branch --show-current)

  git worktree add "${WORKTREE_DIR}"

  pushd ..
  rsync -avq --delete --exclude '.git' ./ "${WORKTREE_DIR}"/
  popd || exit
else
  # Set to true when deploying a different branch since we will likely need to refresh the terraform folders.
  CLEAR_TF="true"
  git worktree add "${WORKTREE_DIR}" "${BRANCH_NAME}"
fi

if [ "${COPY_LIVE}" == "true" ]; then
  if [ -n "${BRANCH_NAME}" ]; then
    rsync -avq ./ "${WORKTREE_DIR}"/live/
  fi
  if [ "${CLEAR_TF}" == "true" ]; then
    find "${WORKTREE_DIR}" -name '.terra*' -exec rm -rf {} +
  fi
else
  INITIAL_CMD="cd live && ./generate_live_env.sh && cd .. &&"
fi

LOG_DIR="logs/${BRANCH_NAME}/${AWS_REGION}/${ENV}/${COMMAND_SUFFIX}"
LOG_FILE="${LOG_DIR}/${TIMESTAMP}.log"
CONTAINER_NAME="${BRANCH_NAME}_${AWS_REGION}_${ENV}_${COMMAND_SUFFIX}"

mkdir -p "$LOG_DIR"

docker build -t terraform-live-env:latest .

# Run the Terragrunt command in the Docker container
container_id=$(docker run -d \
  -e AWS_PROFILE="$AWS_PROFILE" \
  -e AWS_REGION="$AWS_REGION" \
  $ADDL_ENV \
  --name "${CONTAINER_NAME}" \
  -v "${WORKTREE_DIR}:/data" \
  -v ~/.aws:/root/.aws \
  terraform-live-env:latest sh -c "${INITIAL_CMD} cd live/standard/${AWS_REGION}/${ENV} && terragrunt ${TERRAGRUNT_COMMAND} --terragrunt-non-interactive")

# Fetch the logs from the container and write them to a log file
docker logs -f "$container_id" > "$LOG_FILE" 2>&1 &
echo "To follow logs:"
echo "tail -f \"$LOG_FILE\""
echo "tail -f \"$LOG_FILE\"" | pbcopy