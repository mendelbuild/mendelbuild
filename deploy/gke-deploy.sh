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
