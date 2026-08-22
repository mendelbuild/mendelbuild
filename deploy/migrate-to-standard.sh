#!/bin/bash
set -e

# Migrate from GKE Autopilot to GKE Standard
#
# This script:
# 1. Creates a new Standard cluster
# 2. Copies secrets from the old cluster
# 3. Deploys Postgres and Mendel
# 4. Sets up Ingress
#
# Usage:
#   export GCP_PROJECT=your-project-id
#   ./deploy/migrate-to-standard.sh

: "${GCP_PROJECT:?Set GCP_PROJECT to your GCP project ID}"

REGION=${REGION:-us-central1}
ZONE=${ZONE:-us-central1-a}
OLD_CLUSTER=${OLD_CLUSTER:-mendel-cluster}
NEW_CLUSTER=${NEW_CLUSTER:-mendel-standard}

echo "=== Migration: Autopilot -> Standard ==="
echo "Project: $GCP_PROJECT"
echo "Old cluster: $OLD_CLUSTER"
echo "New cluster: $NEW_CLUSTER"
echo ""

# Step 1: Save secrets from old cluster
echo "=== Step 1: Saving secrets from old cluster ==="
gcloud container clusters get-credentials "$OLD_CLUSTER" --region "$REGION" --project "$GCP_PROJECT"

# Export the mendel-secret
kubectl get secret mendel-secret -n mendel -o yaml > /tmp/mendel-secret.yaml
echo "Saved mendel-secret to /tmp/mendel-secret.yaml"

# Step 2: Create new Standard cluster
echo ""
echo "=== Step 2: Creating Standard cluster ==="
gcloud container clusters create "$NEW_CLUSTER" \
    --project "$GCP_PROJECT" \
    --zone "$ZONE" \
    --num-nodes 1 \
    --machine-type e2-medium \
    --disk-size 50 \
    --enable-autoscaling \
    --min-nodes 1 \
    --max-nodes 3 \
    --no-enable-master-authorized-networks \
    --workload-pool="${GCP_PROJECT}.svc.id.goog"

echo "Cluster created. Getting credentials..."
gcloud container clusters get-credentials "$NEW_CLUSTER" --zone "$ZONE" --project "$GCP_PROJECT"

# Step 3: Create namespace and apply secrets
echo ""
echo "=== Step 3: Setting up namespace and secrets ==="
kubectl apply -f deploy/k8s/namespace.yaml

# Remove resource version and other cluster-specific fields before applying
kubectl apply -f /tmp/mendel-secret.yaml

# Step 4: Deploy Postgres
echo ""
echo "=== Step 4: Deploying Postgres ==="
kubectl apply -f deploy/k8s/postgres.yaml

echo "Waiting for Postgres to be ready..."
kubectl wait --for=condition=ready pod -l app=postgres -n mendel --timeout=120s

# Step 5: Deploy Mendel (with DinD sidecar)
echo ""
echo "=== Step 5: Deploying Mendel ==="

# Build and push new image
TAG="$(git rev-parse --short HEAD)-$(date +%s)"
BUILD_TIME=$(date +%s)
IMAGE="$REGION-docker.pkg.dev/$GCP_PROJECT/mendel/mendel:$TAG"

echo "Building $IMAGE..."
docker build --platform linux/amd64 --build-arg VERSION="$TAG" --build-arg BUILD_TIME="$BUILD_TIME" -t "$IMAGE" .
docker push "$IMAGE"

# Update manifest with real image and apply
sed "s|MENDEL_IMAGE_PLACEHOLDER|$IMAGE|g" deploy/k8s/mendel.yaml | kubectl apply -f -

echo "Waiting for Mendel to be ready..."
kubectl wait --for=condition=ready pod -l app=mendel -n mendel --timeout=180s

# Step 6: Run migrations
echo ""
echo "=== Step 6: Running database migrations ==="
kubectl exec -n mendel deployment/mendel -c mendel -- mendel migrate

# Step 7: Set up Ingress
echo ""
echo "=== Step 7: Setting up Ingress ==="
kubectl apply -f deploy/k8s/ingress.yaml

echo ""
echo "=== Migration Complete ==="
echo ""
echo "New cluster: $NEW_CLUSTER"
echo "Image: $IMAGE"
echo ""
echo "Next steps:"
echo "1. Wait for Ingress IP: kubectl get ingress -n mendel"
echo "2. Update DNS if needed (or wait for managed cert)"
echo "3. Verify: curl https://app-staging.mendel.build/version"
echo "4. Delete old cluster when ready:"
echo "   gcloud container clusters delete $OLD_CLUSTER --region $REGION --project $GCP_PROJECT"
echo ""
echo "To verify DinD is working:"
echo "   kubectl exec -n mendel deployment/mendel -c mendel -- sh -c 'echo \$DOCKER_HOST && docker ps'"
