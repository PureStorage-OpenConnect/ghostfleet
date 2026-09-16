#!/bin/sh
# Fetch and check out a git ref for building/testing — a branch, a tag, or a
# pull request by number. Used by `make checkout` / `make deploy`.
#
#   sh tools/checkout-ref.sh main            # branch from origin (tracks origin/main)
#   sh tools/checkout-ref.sh v1.0.0          # tag
#   sh tools/checkout-ref.sh pr/6            # head of pull request #6 -> local branch pr-6
#   sh tools/checkout-ref.sh pr/6 origin     # explicit remote
#
# Remote selection: branches and tags come from "origin" — in a fork setup
# that is your fork, where your own branches live. Pull requests are opened
# against the upstream repository and GitHub exposes their heads only there
# (refs/pull/N/head), so pr/N uses "upstream" when such a remote exists,
# otherwise "origin". The second argument overrides either choice. PR heads
# are fetched anonymously; no GitHub credentials are needed on the public repo.
#
# Refuses to run with uncommitted changes to tracked files, so nothing is
# lost; untracked files (e.g. deploy/.env) are left alone.
set -eu

REF="${1:?usage: checkout-ref.sh <branch|tag|pr/N> [remote]}"
REMOTE="${2:-}"

cd "$(git rev-parse --show-toplevel)"

if [ -z "$REMOTE" ]; then
    case "$REF" in
        pr/*) if git remote get-url upstream >/dev/null 2>&1; then REMOTE=upstream; else REMOTE=origin; fi ;;
        *)    REMOTE=origin ;;
    esac
fi

if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
    echo "checkout-ref: uncommitted changes in tracked files — commit or stash first:" >&2
    git status --short --untracked-files=no >&2
    exit 1
fi

echo ">> fetching from $REMOTE"
git fetch --quiet --tags --prune "$REMOTE"

case "$REF" in
    pr/*)
        N="${REF#pr/}"
        case "$N" in ''|*[!0-9]*) echo "checkout-ref: bad PR ref '$REF' (want pr/<number>)" >&2; exit 2;; esac
        git fetch --quiet "$REMOTE" "pull/$N/head:refs/heads/pr-$N" --update-head-ok
        git checkout --quiet "pr-$N"
        ;;
    *)
        if git show-ref --quiet --verify "refs/remotes/$REMOTE/$REF"; then
            # Branch: (re)create the local branch on the remote tip.
            git checkout --quiet -B "$REF" "$REMOTE/$REF"
        else
            # Tag or commit.
            git checkout --quiet "$REF"
        fi
        ;;
esac

echo ">> at $(git rev-parse --short HEAD) — $(git describe --tags --always --dirty) ($(git log -1 --format=%s))"
