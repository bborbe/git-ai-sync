#!/bin/sh
# Entrypoint for the git-ai-sync vault sync container.
#
# `git-ai-sync watch` exits(1) when a pull leaves a real conflict: it detects
# the conflict and tells the operator to run `resolve`, it does not resolve it
# itself. In a pod that restart-loops on a conflict the tool can actually fix,
# so this wrapper resolves and then resumes watching.
set -u

export HOME="${HOME:-/tmp}"

VAULT="${VAULT_PATH:-/vault}"
INTERVAL="${GIT_AI_SYNC_INTERVAL:-30}"
STRATEGY="${GIT_AI_SYNC_STRATEGY:-merge}"
RETRY_DELAY="${GIT_AI_SYNC_RETRY_DELAY:-5}"

log() { echo "[entrypoint] $*"; }

log "git-ai-sync ${BUILD_GIT_VERSION:-dev} (commit ${BUILD_GIT_COMMIT:-none}, built ${BUILD_DATE:-unknown})"
log "vault=${VAULT} interval=${INTERVAL}s strategy=${STRATEGY} retry=${RETRY_DELAY}s home=${HOME}"

if [ ! -e "$VAULT/.git" ]; then
  log "FATAL: ${VAULT} is not a git checkout — refusing to start"
  exit 1
fi

# The volume is owned by fsGroup 1000, but the clone may have been made under a
# different uid; without this every git command fails with "dubious ownership".
# Idempotent, because `--add` appends and a re-run would otherwise stack
# duplicate entries.
if ! git config --global --get-all safe.directory | grep -qxF "$VAULT"; then
  git config --global --add safe.directory "$VAULT"
fi

# Commit identity belongs to the deployment, not to the image. Both are
# required: a container whose whole job is committing should fail here, at
# startup, rather than at the first commit with git's "Please tell me who you
# are" — by which point the failure surfaces as a sync error instead.
if [ -z "${GIT_USER_NAME:-}" ] || [ -z "${GIT_USER_EMAIL:-}" ]; then
  log "FATAL: GIT_USER_NAME and GIT_USER_EMAIL must both be set — they are the identity of every commit this container makes"
  exit 1
fi
git config --global user.name "$GIT_USER_NAME"
git config --global user.email "$GIT_USER_EMAIL"

# git-ai-sync writes its instance lock to the repository ROOT and stages with
# `git add .`, so an untracked .git-ai-sync.lock would be committed into the
# vault. .git/info/exclude is local-only and never committed — unlike
# .gitignore, which would itself add a file to the vault.
EXCLUDE="$VAULT/.git/info/exclude"
if ! grep -qxF '.git-ai-sync.lock' "$EXCLUDE" 2>/dev/null; then
  echo '.git-ai-sync.lock' >> "$EXCLUDE"
  log "excluded .git-ai-sync.lock via ${EXCLUDE}"
fi

WATCH_PID=""
stop() {
  log "signal received — stopping"
  [ -n "$WATCH_PID" ] && kill "$WATCH_PID" 2>/dev/null
  exit 0
}
trap stop TERM INT

while true; do
  git-ai-sync watch "$VAULT" --strategy "$STRATEGY" --interval "$INTERVAL" &
  WATCH_PID=$!
  wait "$WATCH_PID"
  rc=$?

  if [ "$rc" -ne 0 ]; then
    log "watch exited rc=${rc} — attempting conflict resolution"
    git-ai-sync resolve "$VAULT" || log "resolve failed rc=$? — resuming watch"
  fi
  sleep "$RETRY_DELAY"
done
