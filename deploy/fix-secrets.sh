#!/bin/bash
set -e

# Fix missing postgres-secret and update mendel-secret with correct DB URL
#
# Run this after migrate-to-standard.sh fails on postgres-secret

echo "=== Fixing secrets ==="

# Generate new postgres password
PG_PASSWORD=$(openssl rand -base64 24 | tr -d '/+=' | head -c 24)
echo "Generated Postgres password"

# Create postgres-secret
kubectl create secret generic postgres-secret -n mendel \
  --from-literal=username=mendel \
  --from-literal=password="$PG_PASSWORD" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "Created postgres-secret"

# Extract existing values from mendel-secret
ANTHROPIC_KEY=$(kubectl get secret mendel-secret -n mendel -o jsonpath='{.data.anthropic-api-key}' | base64 -d)
GOOGLE_CLIENT_ID=$(kubectl get secret mendel-secret -n mendel -o jsonpath='{.data.google-client-id}' | base64 -d)
GOOGLE_CLIENT_SECRET=$(kubectl get secret mendel-secret -n mendel -o jsonpath='{.data.google-client-secret}' | base64 -d)
BASE_URL=$(kubectl get secret mendel-secret -n mendel -o jsonpath='{.data.base-url}' | base64 -d)
SESSION_SECRET=$(kubectl get secret mendel-secret -n mendel -o jsonpath='{.data.session-secret}' | base64 -d)

# Recreate mendel-secret with correct database URL
kubectl create secret generic mendel-secret -n mendel \
  --from-literal=database-url="postgres://mendel:${PG_PASSWORD}@postgres:5432/mendel?sslmode=disable" \
  --from-literal=anthropic-api-key="$ANTHROPIC_KEY" \
  --from-literal=google-client-id="$GOOGLE_CLIENT_ID" \
  --from-literal=google-client-secret="$GOOGLE_CLIENT_SECRET" \
  --from-literal=base-url="$BASE_URL" \
  --from-literal=session-secret="$SESSION_SECRET" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "Updated mendel-secret with new database URL"

# Restart postgres to pick up the secret
echo "Restarting Postgres..."
kubectl delete pod postgres-0 -n mendel --ignore-not-found
kubectl wait --for=condition=ready pod -l app=postgres -n mendel --timeout=120s

echo ""
echo "=== Secrets fixed ==="
echo "Postgres should now be running. Continue with Mendel deployment:"
echo ""
echo "  # Build and deploy Mendel"
echo "  ./deploy/gke-deploy.sh"
echo ""
echo "  # Run migrations"
echo "  kubectl exec -n mendel deployment/mendel -c mendel -- mendel migrate"
