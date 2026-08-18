# Mendel Cloud Hosting Plan

> **Status**: Draft  
> **Last Updated**: 2026-08-18

## Overview

Run Mendel itself in GCP instead of locally, offloading Claude API calls and Docker builds from your laptop. This is separate from the "variation deployment" plan (deploying user code to cloud) — this is about hosting Mendel.

## Phases

1. **Auth** — Google OAuth, users table, project membership (many:many)
2. **GKE Deployment** — Mendel + Postgres in a small GKE cluster
3. **Docker Builds** — Run variation code generation builds in-cluster

---

## Phase 1: Authentication & Multi-User

### Goals

- Users sign in with Google
- Projects have multiple members (owner, member roles)
- Works identically on localhost and in production

### Database Schema

```sql
-- Migration 017_users_and_auth.up.sql

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    name TEXT,
    picture_url TEXT,
    google_id TEXT UNIQUE,  -- Google's sub claim
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_google_id ON users(google_id);

CREATE TABLE project_members (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'member')),
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, user_id)
);

CREATE INDEX idx_project_members_user ON project_members(user_id);
CREATE INDEX idx_project_members_project ON project_members(project_id);

CREATE TABLE sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,  -- SHA-256 of session token
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sessions_token ON sessions(token_hash);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
```

### OAuth Flow

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Browser   │     │   Mendel    │     │   Google    │
└──────┬──────┘     └──────┬──────┘     └──────┬──────┘
       │                   │                   │
       │ GET /auth/login   │                   │
       │──────────────────>│                   │
       │                   │                   │
       │ 302 → Google      │                   │
       │<──────────────────│                   │
       │                   │                   │
       │ Sign in with Google                   │
       │──────────────────────────────────────>│
       │                   │                   │
       │ 302 → /auth/callback?code=...         │
       │<──────────────────────────────────────│
       │                   │                   │
       │ GET /auth/callback│                   │
       │──────────────────>│                   │
       │                   │ Exchange code     │
       │                   │──────────────────>│
       │                   │ {access_token}    │
       │                   │<──────────────────│
       │                   │                   │
       │                   │ GET /userinfo     │
       │                   │──────────────────>│
       │                   │ {email,name,sub}  │
       │                   │<──────────────────│
       │                   │                   │
       │ Set-Cookie: session=<token>           │
       │ 302 → /                               │
       │<──────────────────│                   │
       │                   │                   │
```

### Implementation

**Files to create:**

```
internal/
  auth/
    oauth.go       # Google OAuth client setup, token exchange
    session.go     # Session creation, validation, middleware
    handlers.go    # /auth/login, /auth/callback, /auth/logout
```

**Config (env vars for Mendel server):**

These are read by the Go server at startup. Set in your shell for local dev, in Kubernetes Secrets for GKE.

```bash
GOOGLE_CLIENT_ID=xxx.apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=xxx
MENDEL_BASE_URL=http://localhost:8080  # or https://mendel.example.com
SESSION_SECRET=<random-32-bytes-hex>   # for signing/encrypting cookies
```

**Deployment approach:** `gcloud` CLI + `kubectl` + shell scripts. No Terraform — we'll script everything so it's copy-paste reproducible. See Phase 2 for the full command sequence.

**Middleware:**

```go
// RequireAuth middleware — redirects to /auth/login if no valid session
func (s *Server) RequireAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        user, err := s.auth.UserFromRequest(r)
        if err != nil {
            http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
            return
        }
        ctx := context.WithValue(r.Context(), userContextKey, user)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

// RequireProjectAccess — checks user is member of project
func (s *Server) RequireProjectAccess(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        user := UserFromContext(r.Context())
        projectID := chi.URLParam(r, "projectID")
        
        isMember, err := s.db.IsProjectMember(r.Context(), projectID, user.ID)
        if err != nil || !isMember {
            http.Error(w, "Forbidden", http.StatusForbidden)
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

**Router changes:**

```go
r := chi.NewRouter()

// Public routes
r.Get("/auth/login", s.handleAuthLogin)
r.Get("/auth/callback", s.handleAuthCallback)
r.Get("/auth/logout", s.handleAuthLogout)

// Protected routes
r.Group(func(r chi.Router) {
    r.Use(s.RequireAuth)
    
    r.Get("/", s.handleHome)  // List user's projects
    r.Post("/projects", s.handleCreateProject)
    
    r.Route("/p/{projectID}", func(r chi.Router) {
        r.Use(s.RequireProjectAccess)
        r.Get("/", s.handleProjectHome)
        r.Get("/strategy", s.handleStrategy)
        // ... existing routes
    })
})
```

### UI Changes

**Login page** (`/auth/login` when not authenticated):

```
┌─────────────────────────────────────┐
│                                     │
│            MendelBuild              │
│                                     │
│   ┌─────────────────────────────┐   │
│   │  Sign in with Google    G   │   │
│   └─────────────────────────────┘   │
│                                     │
└─────────────────────────────────────┘
```

**Header** (when authenticated):

```
┌─────────────────────────────────────────────────────────┐
│ MendelBuild                    ben@example.com [Logout] │
└─────────────────────────────────────────────────────────┘
```

**Home page** (`/` — list of user's projects):

```
┌─────────────────────────────────────────────────────────┐
│ Your Projects                         [+ New Project]   │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  acme-webapp                              owner         │
│     3 active hops · Last activity 2h ago                │
│                                                         │
│  internal-tools                           member        │
│     1 active hop · Last activity 1d ago                 │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

**Project settings** — add "Members" section:

```
┌─────────────────────────────────────────────────────────┐
│ Members                                                 │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ben@example.com              owner                     │
│  alice@example.com            member        [Remove]    │
│                                                         │
│  [+ Invite Member]                                      │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Google Cloud Setup (OAuth credentials via CLI)

Unfortunately, OAuth consent screen and credentials still require the console UI — Google doesn't expose these via `gcloud`. But it's a one-time setup:

1. Go to [Google Cloud Console > APIs & Services > OAuth consent screen](https://console.cloud.google.com/apis/credentials/consent)
2. Create consent screen: External, app name "Mendel", your email as support contact
3. Go to [Credentials](https://console.cloud.google.com/apis/credentials) > Create Credentials > OAuth 2.0 Client ID
4. Application type: Web application
5. Authorized redirect URIs:
   - `http://localhost:8080/auth/callback`
   - `https://mendel.yourdomain.com/auth/callback`
6. Copy Client ID and Client Secret

Everything else (GKE cluster, secrets, DNS, deployment) is fully scriptable — see Phase 2.

### Migration Path

Since Mendel is in prototype (per CLAUDE.md), we can:
1. Add the new tables
2. Wrap all existing routes in `RequireAuth`
3. Auto-create a user on first Google sign-in

**Assigning ownership of existing projects** is a CLI command, not automatic:

```bash
mendel assign-owner --email bhs@gmail.com
```

This finds or creates the user by email, then sets them as owner on all projects that currently have no owner. Explicit, auditable, and only runs when you invoke it.

---

## Phase 2: GKE Deployment

### Goals

- Run Mendel in GKE with persistent storage
- Self-hosted Postgres (cheaper than Cloud SQL)
- Git work directories on persistent disk
- HTTPS with managed cert

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         GKE Cluster                             │
│                                                                 │
│  ┌─────────────────┐    ┌─────────────────┐                     │
│  │  Ingress/LB     │    │   Mendel Pod    │                     │
│  │  (HTTPS)        │───>│                 │                     │
│  │                 │    │  - Go server    │                     │
│  └─────────────────┘    │  - /work mount  │──┐                  │
│                         └────────┬────────┘  │                  │
│                                  │           │                  │
│                                  v           v                  │
│                         ┌────────────┐  ┌────────────┐          │
│                         │ Postgres   │  │ PVC: work  │          │
│                         │ (StatefulSet)│ │ (10GB SSD) │          │
│                         │            │  │ git clones │          │
│                         │ PVC: data  │  └────────────┘          │
│                         │ (10GB SSD) │                          │
│                         └────────────┘                          │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### GCP Setup Commands

All cluster and infrastructure setup via CLI. Run once to bootstrap.

```bash
# Set your project
export GCP_PROJECT=your-gcp-project-id
export REGION=us-central1
export ZONE=us-central1-a
export DOMAIN=mendel.yourdomain.com

gcloud config set project $GCP_PROJECT

# Enable required APIs
gcloud services enable container.googleapis.com
gcloud services enable artifactregistry.googleapis.com

# Create GKE Autopilot cluster (simplest, pay-per-pod)
gcloud container clusters create-auto mendel-cluster \
    --region=$REGION

# Or: GKE Standard with a small node (cheaper but more manual)
# gcloud container clusters create mendel-cluster \
#     --zone=$ZONE \
#     --num-nodes=1 \
#     --machine-type=e2-small \
#     --disk-size=30GB

# Get credentials for kubectl
gcloud container clusters get-credentials mendel-cluster --region=$REGION

# Create Artifact Registry for Docker images
gcloud artifacts repositories create mendel \
    --repository-format=docker \
    --location=$REGION

# Reserve a static IP for the load balancer
gcloud compute addresses create mendel-ip --global

# Get the IP (you'll point DNS here)
gcloud compute addresses describe mendel-ip --global --format='value(address)'

# Create secrets (replace with real values)
kubectl create namespace mendel

kubectl create secret generic postgres-secret \
    --namespace=mendel \
    --from-literal=username=mendel \
    --from-literal=password=$(openssl rand -base64 24)

kubectl create secret generic mendel-secret \
    --namespace=mendel \
    --from-literal=database-url="postgres://mendel:PASSWORD@postgres:5432/mendel?sslmode=disable" \
    --from-literal=anthropic-api-key="sk-ant-..." \
    --from-literal=google-client-id="xxx.apps.googleusercontent.com" \
    --from-literal=google-client-secret="xxx" \
    --from-literal=session-secret=$(openssl rand -hex 32)
```

**DNS setup:** Point `$DOMAIN` to the static IP from above. If using Cloud DNS:

```bash
# Create zone (if not exists)
gcloud dns managed-zones create mendel-zone \
    --dns-name="yourdomain.com." \
    --description="Mendel domain"

# Add A record
gcloud dns record-sets create $DOMAIN. \
    --zone=mendel-zone \
    --type=A \
    --ttl=300 \
    --rrdatas=$(gcloud compute addresses describe mendel-ip --global --format='value(address)')
```

**Build and push Mendel image:**

```bash
# Configure Docker for Artifact Registry
gcloud auth configure-docker $REGION-docker.pkg.dev

# Build and push
docker build -t $REGION-docker.pkg.dev/$GCP_PROJECT/mendel/mendel:latest .
docker push $REGION-docker.pkg.dev/$GCP_PROJECT/mendel/mendel:latest
```

### Kubernetes Resources

**Namespace:**
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: mendel
```

**Postgres StatefulSet:**
```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: postgres
  namespace: mendel
spec:
  serviceName: postgres
  replicas: 1
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
      - name: postgres
        image: postgres:15
        env:
        - name: POSTGRES_DB
          value: mendel
        - name: POSTGRES_USER
          valueFrom:
            secretKeyRef:
              name: postgres-secret
              key: username
        - name: POSTGRES_PASSWORD
          valueFrom:
            secretKeyRef:
              name: postgres-secret
              key: password
        ports:
        - containerPort: 5432
        volumeMounts:
        - name: data
          mountPath: /var/lib/postgresql/data
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: ["ReadWriteOnce"]
      resources:
        requests:
          storage: 10Gi
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: mendel
spec:
  selector:
    app: postgres
  ports:
  - port: 5432
```

**Mendel Deployment:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mendel
  namespace: mendel
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mendel
  template:
    metadata:
      labels:
        app: mendel
    spec:
      containers:
      - name: mendel
        image: us-central1-docker.pkg.dev/YOUR_PROJECT/mendel/mendel:latest
        env:
        - name: MENDEL_DB_URL
          valueFrom:
            secretKeyRef:
              name: mendel-secret
              key: database-url
        - name: ANTHROPIC_API_KEY
          valueFrom:
            secretKeyRef:
              name: mendel-secret
              key: anthropic-api-key
        - name: GOOGLE_CLIENT_ID
          valueFrom:
            secretKeyRef:
              name: mendel-secret
              key: google-client-id
        - name: GOOGLE_CLIENT_SECRET
          valueFrom:
            secretKeyRef:
              name: mendel-secret
              key: google-client-secret
        - name: MENDEL_BASE_URL
          value: "https://mendel.yourdomain.com"
        - name: SESSION_SECRET
          valueFrom:
            secretKeyRef:
              name: mendel-secret
              key: session-secret
        - name: MENDEL_WORK_DIR
          value: "/work"
        ports:
        - containerPort: 8080
        volumeMounts:
        - name: work
          mountPath: /work
      volumes:
      - name: work
        persistentVolumeClaim:
          claimName: mendel-work
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mendel-work
  namespace: mendel
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 10Gi
---
apiVersion: v1
kind: Service
metadata:
  name: mendel
  namespace: mendel
spec:
  selector:
    app: mendel
  ports:
  - port: 80
    targetPort: 8080
```

**Ingress (with managed cert):**
```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: mendel
  namespace: mendel
  annotations:
    kubernetes.io/ingress.class: "gce"
    networking.gke.io/managed-certificates: "mendel-cert"
spec:
  rules:
  - host: mendel.yourdomain.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: mendel
            port:
              number: 80
---
apiVersion: networking.gke.io/v1
kind: ManagedCertificate
metadata:
  name: mendel-cert
  namespace: mendel
spec:
  domains:
  - mendel.yourdomain.com
```

### Dockerfile

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o mendel ./cmd/mendel

FROM alpine:3.19
RUN apk add --no-cache ca-certificates git
COPY --from=builder /app/mendel /usr/local/bin/mendel
COPY --from=builder /app/schema /schema
EXPOSE 8080
CMD ["mendel", "serve"]
```

### Deploy Script

All manifests live in `deploy/k8s/`. Apply in order:

```bash
# From repo root
kubectl apply -f deploy/k8s/namespace.yaml
kubectl apply -f deploy/k8s/postgres.yaml
kubectl apply -f deploy/k8s/mendel.yaml
kubectl apply -f deploy/k8s/ingress.yaml

# Or all at once:
kubectl apply -f deploy/k8s/

# Watch rollout
kubectl rollout status deployment/mendel -n mendel

# Run migrations (once postgres is up)
kubectl exec -n mendel deployment/mendel -- mendel migrate

# Assign ownership of existing projects
kubectl exec -n mendel deployment/mendel -- mendel assign-owner --email bhs@gmail.com
```

### Estimated Costs

| Resource | Spec | Monthly Cost |
|----------|------|--------------|
| GKE Autopilot | Small workloads | ~$75 base |
| PVC (2x 10GB SSD) | Standard | ~$2 |
| Load Balancer | Regional | ~$18 |
| **Total** | | **~$95/mo** |

Or with GKE Standard (1x e2-small node): ~$25/mo but more manual management.

---

## Phase 3: Docker Builds in Cluster

### Problem

Mendel needs to run Docker builds for variation code generation. Options:

1. **Docker-in-Docker (DinD)** in the Mendel pod — simplest but security concerns
2. **Kaniko** — builds images without Docker daemon, designed for K8s
3. **Cloud Build** — GCP-managed, but adds API call latency

### Recommendation: Kaniko

Kaniko runs as a container, builds images, pushes to registry — no Docker daemon needed.

**Flow:**
1. Mendel creates a Kubernetes Job with Kaniko
2. Kaniko builds from the variation's git branch
3. Image pushed to GCR
4. Mendel monitors Job completion
5. (For demo) Mendel deploys the image to a preview namespace

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: build-variation-abc123
  namespace: mendel-builds
spec:
  template:
    spec:
      containers:
      - name: kaniko
        image: gcr.io/kaniko-project/executor:latest
        args:
        - "--dockerfile=/workspace/Dockerfile"
        - "--context=git://github.com/user/repo.git#refs/heads/variation-abc123"
        - "--destination=us-central1-docker.pkg.dev/YOUR_PROJECT/mendel/user-app:variation-abc123"
        volumeMounts:
        - name: kaniko-secret
          mountPath: /kaniko/.docker
      restartPolicy: Never
      volumes:
      - name: kaniko-secret
        secret:
          secretName: gcr-credentials
  backoffLimit: 2
```

This can wait until Phases 1-2 are working.

---

## Implementation Order

### Phase 1A: Auth Foundation (local-only)
1. Migration 017: users, project_members, sessions tables
2. `internal/auth/` package: OAuth client, session management
3. Auth handlers: /auth/login, /auth/callback, /auth/logout
4. RequireAuth middleware wrapping all routes
5. Update templates: login page, user header, logout link

### Phase 1B: Project Membership
1. Home page showing user's projects
2. Project settings: member list, invite flow
3. RequireProjectAccess middleware
4. DB queries: IsProjectMember, AddProjectMember, etc.

### Phase 2: GKE Deployment
1. Dockerfile for Mendel
2. Kubernetes manifests (postgres, mendel, ingress)
3. GCP setup: cluster, DNS, OAuth redirect URI
4. CI/CD: build and push on main

### Phase 3: In-Cluster Builds
1. Kaniko Job template
2. Build orchestration from Mendel
3. Preview deployments for variations

---

## Success Criteria

**Phase 1:**
- Can sign in with Google locally
- Only see projects you're a member of
- Can invite others to projects

**Phase 2:**
- Mendel accessible at https://mendel.yourdomain.com
- Data persists across pod restarts
- Same experience as local

**Phase 3:**
- Variation builds run in GKE, not locally
- Build logs visible in Mendel UI
