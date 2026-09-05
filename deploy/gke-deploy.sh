#!/bin/bash
set -e

# Quick deploy script for Mendel (after initial setup)
# Builds, pushes, and updates the deployment.
#
# Usage:
#   export GCP_PROJECT=your-project-id
#   ./deploy/gke-deploy.sh

: "${GCP_PROJECT:?Set GCP_PROJECT to your GCP project ID}"

# Refuse to ship code older than what has landed.
#
# Several sessions work this repo at once, and the checkout that holds main is
# routinely behind origin. Deploying from it looks completely normal -- the build
# succeeds, the rollout succeeds -- and quietly reverts whatever landed in the
# meantime. Nothing downstream would show it.
git fetch -q origin 2>/dev/null || true
BEHIND=$(git rev-list --count HEAD..origin/main 2>/dev/null || echo 0)
if [ "${BEHIND:-0}" -gt 0 ]; then
    echo "HEAD is $BEHIND commit(s) behind origin/main."
    echo "Deploying now would ship code older than what has landed:"
    git log --oneline HEAD..origin/main | head -10 | sed 's/^/  /'
    echo
    echo "  git pull --ff-only origin main"
    echo
    echo "Set ALLOW_STALE=1 to deploy anyway."
    [ -n "${ALLOW_STALE:-}" ] || exit 1
    echo "ALLOW_STALE set; deploying the older code deliberately."
fi

REGION=${REGION:-us-central1}

# Deploy where GCP_PROJECT says, not wherever kubectl happens to point.
#
# Every kubectl call below used the ambient current-context, which is global,
# survives between sessions, and is changed by any `gcloud container clusters
# get-credentials` anyone runs for any reason -- including for an unrelated
# cluster. When that happens this script builds the right image, pushes it to
# the right registry, and then aims the rollout at somebody else's cluster. It
# failed loudly here, on a namespace that did not exist, which was luck: against
# a cluster that happened to have a `mendel` deployment it would have quietly
# deployed staging over it.
#
# So derive the context from GCP_PROJECT and pass it explicitly. The context is
# never switched, because mutating global state to fix a problem caused by
# global state only moves it.
CONTEXTS=$(kubectl config get-contexts -o name | grep "^gke_${GCP_PROJECT}_" || true)
CONTEXT_COUNT=$(printf '%s' "$CONTEXTS" | grep -c . || true)

if [ "${CONTEXT_COUNT:-0}" -eq 0 ]; then
    echo "No kubectl context for project $GCP_PROJECT."
    echo "Get credentials for its cluster first:"
    echo
    echo "  gcloud container clusters get-credentials <cluster> --location <location> --project $GCP_PROJECT"
    echo
    exit 1
fi
if [ "$CONTEXT_COUNT" -gt 1 ]; then
    echo "More than one kubectl context for project $GCP_PROJECT:"
    printf '%s\n' "$CONTEXTS" | sed 's/^/  /'
    echo
    echo "Set KUBE_CONTEXT to the one you mean."
    exit 1
fi

KUBE_CONTEXT=${KUBE_CONTEXT:-$CONTEXTS}
echo "=== Deploying to $KUBE_CONTEXT ==="
kubectl() { command kubectl --context "$KUBE_CONTEXT" "$@"; }
# Include timestamp to ensure unique tag even with uncommitted changes
TAG="$(git rev-parse --short HEAD)-$(date +%s)"
IMAGE="$REGION-docker.pkg.dev/$GCP_PROJECT/mendel/mendel:$TAG"

BUILD_TIME=$(date +%s)

echo "=== Building $IMAGE ==="
docker build --platform linux/amd64 --build-arg VERSION="$TAG" --build-arg BUILD_TIME="$BUILD_TIME" -t "$IMAGE" .

echo "=== Pushing ==="
docker push "$IMAGE"

echo "=== Updating deployment to $TAG ==="
kubectl set image deployment/mendel -n mendel mendel="$IMAGE"

echo "=== Waiting for rollout ==="
kubectl rollout status deployment/mendel -n mendel --timeout=300s

echo "=== Running migrations ==="
kubectl exec -n mendel deployment/mendel -- mendel migrate

echo "=== Running setup ==="
kubectl exec -n mendel deployment/mendel -- mendel setup

# Platform data lives in the database, and `setup` only seeds an empty table, so
# without this a deploy that changes a setup script or its instructions leaves
# the old text in place -- the code ships and the UI never changes. Refreshing is
# quick and idempotent, so it belongs on every deploy rather than in a habit.
echo "=== Refreshing hosting platforms ==="
kubectl exec -n mendel deployment/mendel -- mendel platforms refresh

echo "=== Done ==="
echo "Deployed: $IMAGE"
