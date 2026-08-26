#!/bin/bash
set -e

# Quick deploy script for Mendel (after initial setup)
# Builds, pushes, and updates the deployment.
#
# Usage:
#   export GCP_PROJECT=your-project-id
#   ./deploy/gke-deploy.sh

: "${GCP_PROJECT:?Set GCP_PROJECT to your GCP project ID}"

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

echo "=== Done ==="
echo "Deployed: $IMAGE"
