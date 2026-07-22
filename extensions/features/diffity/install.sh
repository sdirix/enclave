#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Build and install diffity from a pinned git commit, inside the agent image.
#
# Why this shape (see also README.md):
#   - A commit SHA is immutable and content-verified by git, so the build gets
#     exactly the reviewed source — not whatever the npm registry currently
#     serves for "diffity".
#   - `npm ci` installs the exact, hash-checked dependency tree from the
#     committed package-lock.json.
#   - `--ignore-scripts` prevents arbitrary dependency lifecycle scripts from
#     running at build; the bundler esbuild, whose own install step it skips, is
#     rebuilt explicitly. diffity has no native (node-gyp) modules.
#   - Build and launch both use the private agent Node runtime, never the ambient
#     `node` on PATH — the convention every bundled agent tool follows. The
#     user-facing node can be repointed (the node-dev feature aliases its own nvm
#     default; a project .nvmrc selects a version), so pinning keeps diffity on a
#     runtime that meets its `engines` floor (Node >=22.13) regardless of the
#     session. Sessions need no npm/registry access at runtime.
#
# Bumping diffity: review the new commit AND its package-lock.json, then update
# DIFFITY_SHA and rebuild.
set -euo pipefail

DIFFITY_REPO="${DIFFITY_REPO:-https://github.com/sdirix/diffity.git}"
DIFFITY_SHA="${DIFFITY_SHA:-31fb9c88b6dbce34ec4d12910eeea3229a018cc0}"
DIFFITY_DIR="${DIFFITY_DIR:-/opt/diffity}"

command -v git >/dev/null 2>&1 || { echo "diffity: git is required at build time" >&2; exit 1; }

# Resolve the private agent Node runtime and use it for every build step below,
# matching the runtime the launcher pins to. Do not rely on PATH order alone: the
# root install phase happens to expose this runtime first today, but pinning keeps
# the build reproducible regardless.
NODE_DIR="${ENCLAVE_AGENT_NODE_DIR:-/opt/enclave/node}"
NODE_BIN="$NODE_DIR/bin/node"
NPM_BIN="$NODE_DIR/bin/npm"
[ -x "$NODE_BIN" ] || { echo "diffity: private agent node is missing at $NODE_BIN" >&2; exit 1; }
[ -x "$NPM_BIN" ] || { echo "diffity: private agent npm is missing at $NPM_BIN" >&2; exit 1; }
# npm spawns `node` for node-gyp and lifecycle steps; make those resolve to the
# agent runtime as well.
export PATH="$NODE_DIR/bin:$PATH"

echo "diffity: cloning $DIFFITY_REPO @ $DIFFITY_SHA"
rm -rf "$DIFFITY_DIR"
git clone --quiet "$DIFFITY_REPO" "$DIFFITY_DIR"
git -C "$DIFFITY_DIR" checkout --quiet "$DIFFITY_SHA"

# A checkout of a branch/tag would be mutable; verify we are exactly at the
# pinned commit before trusting the tree.
got="$(git -C "$DIFFITY_DIR" rev-parse HEAD)"
if [ "$got" != "$DIFFITY_SHA" ]; then
  echo "diffity: checkout SHA mismatch (got $got, want $DIFFITY_SHA)" >&2
  exit 1
fi

cd "$DIFFITY_DIR"
"$NPM_BIN" ci --ignore-scripts
# esbuild: bundler binary needed for `npm run build`; its own install step is
# skipped by --ignore-scripts, so rebuild it explicitly.
"$NPM_BIN" rebuild esbuild
"$NPM_BIN" run build

# Keep any externalized runtime deps resolvable; drop only dev-only packages to
# trim the layer.
"$NPM_BIN" prune --omit=dev --ignore-scripts

# Launch via a wrapper that pins the agent Node runtime. The bundle's own
# `#!/usr/bin/env node` shebang would otherwise resolve to the session's
# user-facing node (repointed by node-dev or a project .nvmrc), which may not
# meet diffity's required Node version.
BIN="$DIFFITY_DIR/packages/cli/dist/index.js"
[ -f "$BIN" ] || { echo "diffity: build did not produce $BIN" >&2; exit 1; }
cat > /usr/local/bin/diffity <<EOF
#!/bin/sh
exec "$NODE_BIN" "$BIN" "\$@"
EOF
chmod +x /usr/local/bin/diffity

# Drop VCS metadata and the remote config from the image layer.
rm -rf "$DIFFITY_DIR/.git"

echo "diffity: installed $(/usr/local/bin/diffity --version 2>/dev/null || echo "@$DIFFITY_SHA")"
