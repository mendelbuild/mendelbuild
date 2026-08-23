#!/bin/bash
set -e

# GKE Setup Script for Mendel
# This script sets up a GKE cluster with Postgres and deploys Mendel.
#
# Prerequisites:
#   - gcloud CLI installed and authenticated
#   - kubectl installed
#   - Docker installed (for building images)
#
# Usage:
#   export GCP_PROJECT=your-project-id
#   export MENDEL_DOMAIN=mendel.yourdomain.com
#   export ANTHROPIC_API_KEY=sk-ant-...
#   export GOOGLE_CLIENT_ID=xxx.apps.googleusercontent.com
#   export GOOGLE_CLIENT_SECRET=xxx
#   ./deploy/gke-setup.sh

echo "=== Mendel GKE Setup ==="

# Check required environment variables
: "${GCP_PROJECT:?Set GCP_PROJECT to your GCP project ID}"
: "${MENDEL_DOMAIN:?Set MENDEL_DOMAIN to your domain (e.g., mendel.example.com)}"
: "${ANTHROPIC_API_KEY:?Set ANTHROPIC_API_KEY}"
: "${GOOGLE_CLIENT_ID:?Set GOOGLE_CLIENT_ID}"
: "${GOOGLE_CLIENT_SECRET:?Set GOOGLE_CLIENT_SECRET}"

REGION=${REGION:-us-central1}
CLUSTER_NAME=${CLUSTER_NAME:-mendel-cluster}

echo "Project: $GCP_PROJECT"
echo "Region: $REGION"
echo "Domain: $MENDEL_DOMAIN"
echo ""

# Set project
gcloud config set project "$GCP_PROJECT"

# Enable required APIs
echo "=== Enabling APIs ==="
gcloud services enable container.googleapis.com
gcloud services enable artifactregistry.googleapis.com

# Check if cluster exists
if gcloud container clusters describe "$CLUSTER_NAME" --region="$REGION" &>/dev/null; then
    echo "=== Cluster $CLUSTER_NAME already exists ==="
else
    echo "=== Creating GKE Autopilot cluster ==="
    gcloud container clusters create-auto "$CLUSTER_NAME" \
        --region="$REGION"
fi

# Get credentials
echo "=== Getting cluster credentials ==="
gcloud container clusters get-credentials "$CLUSTER_NAME" --region="$REGION"

# Create Artifact Registry repository if it doesn't exist
if gcloud artifacts repositories describe mendel --location="$REGION" &>/dev/null; then
    echo "=== Artifact Registry 'mendel' already exists ==="
else
    echo "=== Creating Artifact Registry ==="
    gcloud artifacts repositories create mendel \
        --repository-format=docker \
        --location="$REGION"
fi

# Reserve static IP if it doesn't exist
if gcloud compute addresses describe mendel-ip --global &>/dev/null; then
    echo "=== Static IP 'mendel-ip' already exists ==="
else
    echo "=== Reserving static IP ==="
    gcloud compute addresses create mendel-ip --global
fi

STATIC_IP=$(gcloud compute addresses describe mendel-ip --global --format='value(address)')
echo "Static IP: $STATIC_IP"
echo ""
echo ">>> Point your DNS ($MENDEL_DOMAIN) to: $STATIC_IP <<<"
echo ""

# Build and push image (linux/amd64 for GKE)
echo "=== Building and pushing Mendel image ==="
IMAGE="$REGION-docker.pkg.dev/$GCP_PROJECT/mendel/mendel:latest"
gcloud auth configure-docker "$REGION-docker.pkg.dev" --quiet
docker build --platform linux/amd64 -t "$IMAGE" .
docker push "$IMAGE"

# Create namespace
echo "=== Creating namespace ==="
kubectl apply -f deploy/k8s/namespace.yaml

# Generate passwords and keys
PG_PASSWORD=$(openssl rand -base64 24 | tr -dc 'a-zA-Z0-9' | head -c 24)
SESSION_SECRET=$(openssl rand -hex 32)
CREDENTIAL_KEY=$(openssl rand -base64 32)

# Create secrets
echo "=== Creating secrets ==="
kubectl create secret generic postgres-secret \
    --namespace=mendel \
    --from-literal=username=mendel \
    --from-literal=password="$PG_PASSWORD" \
    --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic mendel-secret \
    --namespace=mendel \
    --from-literal=database-url="postgres://mendel:$PG_PASSWORD@postgres:5432/mendel?sslmode=disable" \
    --from-literal=anthropic-api-key="$ANTHROPIC_API_KEY" \
    --from-literal=google-client-id="$GOOGLE_CLIENT_ID" \
    --from-literal=google-client-secret="$GOOGLE_CLIENT_SECRET" \
    --from-literal=base-url="https://$MENDEL_DOMAIN" \
    --from-literal=session-secret="$SESSION_SECRET" \
    --from-literal=credential-key="$CREDENTIAL_KEY" \
    --dry-run=client -o yaml | kubectl apply -f -

# Deploy Postgres
echo "=== Deploying Postgres ==="
kubectl apply -f deploy/k8s/postgres.yaml

# Wait for Postgres to be ready
echo "=== Waiting for Postgres to be ready ==="
kubectl rollout status statefulset/postgres -n mendel --timeout=300s

# Deploy Mendel (with image substitution)
echo "=== Deploying Mendel ==="
sed "s|MENDEL_IMAGE_PLACEHOLDER|$IMAGE|g" deploy/k8s/mendel.yaml | kubectl apply -f -

# Wait for Mendel to be ready
echo "=== Waiting for Mendel to be ready ==="
kubectl rollout status deployment/mendel -n mendel --timeout=300s

# Run migrations
echo "=== Running migrations ==="
kubectl exec -n mendel deployment/mendel -- mendel migrate

# Deploy ingress (with domain substitution)
echo "=== Deploying Ingress ==="
sed "s|MENDEL_MENDEL_DOMAIN_PLACEHOLDER|$MENDEL_DOMAIN|g" deploy/k8s/ingress.yaml | kubectl apply -f -

echo ""
echo "=== Setup complete! ==="
echo ""
echo "Static IP: $STATIC_IP"
echo "Domain: https://$MENDEL_DOMAIN"
echo ""
echo "Next steps:"
echo "1. Point DNS for $MENDEL_DOMAIN to $STATIC_IP"
echo "2. Wait for managed certificate to provision (can take 15-60 minutes)"
echo "3. Visit https://$MENDEL_DOMAIN"
echo ""
echo "To assign ownership of existing projects:"
echo "  kubectl exec -n mendel deployment/mendel -- mendel assign-owner --email YOUR_EMAIL"
echo ""
echo "To check certificate status:"
echo "  kubectl describe managedcertificate mendel-cert -n mendel"
