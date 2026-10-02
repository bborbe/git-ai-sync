# git-ai-sync — the vault sync container for the Data Assistant pod.
#
# Replaces the hand-rolled vault-sync.sh loop (fetch, `merge --ff-only`,
# commit, push, every failure swallowed by `|| true`) with git-ai-sync's watch
# mode: debounce-gated polling, a real merge pull, and AI conflict resolution
# through the Claude Code CLI.
#
# Two runtimes are required. git-ai-sync is Python (>=3.14) and drives the git
# binary; its conflict resolver drives the `claude` CLI, which is Node. The
# base is node:22-slim so the CLI's runtime is native here; the Python
# interpreter comes from uv, which manages its own 3.14 (this base's distro
# python is 3.11, too old for the package).

FROM node:22-slim

ARG BUILD_GIT_VERSION=dev
ARG BUILD_GIT_COMMIT=none
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="git-ai-sync"
LABEL org.opencontainers.image.description="Automatic Git repository sync with AI-powered conflict resolution"
LABEL org.opencontainers.image.vendor="Benjamin Borbe"
LABEL org.opencontainers.image.source="https://github.com/bborbe/git-ai-sync"
LABEL org.opencontainers.image.version="${BUILD_GIT_VERSION}"
LABEL org.opencontainers.image.created="${BUILD_DATE}"
LABEL org.opencontainers.image.revision="${BUILD_GIT_COMMIT}"

# git: git-ai-sync drives the git binary directly. openssh-client: the vault
# remote is reached over an SSH deploy key. curl/ca-certificates: TLS.
RUN apt-get update \
 && apt-get install -y --no-install-recommends git openssh-client curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# uv — a static binary, so it needs no runtime of its own. The tool below is
# built from a uv-managed Python 3.14 because git-ai-sync requires >=3.14.
COPY --from=ghcr.io/astral-sh/uv:0.12.19 /uv /uvx /usr/local/bin/

# Fixed prefixes, deliberately not under HOME: the pod points HOME at /tmp,
# which is an emptyDir and does not survive a restart. Pinning the managed
# interpreter outside HOME also means the container never downloads a Python
# at runtime — a pod with no egress still starts.
ENV UV_PYTHON_INSTALL_DIR=/opt/uv/python \
    UV_TOOL_DIR=/opt/uv/tools \
    UV_TOOL_BIN_DIR=/usr/local/bin \
    UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1 \
    UV_PYTHON=3.14

RUN uv python install 3.14

WORKDIR /src
COPY pyproject.toml uv.lock README.md ./
COPY src/ ./src/

# pyproject.toml versions dynamically via hatch-vcs. The build context carries
# no usable .git — a worktree's .git is a pointer file Docker cannot follow —
# so hatch-vcs falls back to the `fallback-version` declared there. That is
# acceptable: the real version travels on the OCI labels above, and
# git_ai_sync/__init__.py reads the *installed* distribution metadata rather
# than a generated _version.py, so no extra file has to be written.
RUN uv tool install --no-cache .

# The conflict resolver spawns the Claude Code CLI.
RUN npm install -g @anthropic-ai/claude-code

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh

# node:22-slim already ships uid/gid 1000 as `node` — exactly the
# runAsUser/runAsGroup/fsGroup the pod sets, and the uid its read-only
# ssh-deploy-key mount under /home/node/.ssh is written for. The pod also sets
# readOnlyRootFilesystem, so nothing may need to write outside /vault and /tmp.
USER node

ENV HOME=/tmp \
    VAULT_PATH=/vault \
    GIT_AI_SYNC_INTERVAL=30 \
    GIT_AI_SYNC_STRATEGY=merge

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
