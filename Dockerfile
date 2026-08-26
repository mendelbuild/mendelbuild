FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG BUILD_TIME=0
RUN CGO_ENABLED=0 go build -ldflags="-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}" -o mendel ./cmd/mendel

FROM alpine:3.19

# Base tools
RUN apk add --no-cache ca-certificates git docker-cli docker-cli-compose bash curl python3 py3-pip

# Install flyctl (Fly.io CLI)
RUN curl -L https://fly.io/install.sh | FLYCTL_INSTALL=/usr/local sh

# Install kubectl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
    install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl && \
    rm kubectl

# Install gcloud CLI (using the standalone archive for Alpine)
RUN curl -O https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-linux-x86_64.tar.gz && \
    tar -xf google-cloud-cli-linux-x86_64.tar.gz && \
    ./google-cloud-sdk/install.sh --quiet --path-update=false && \
    mv google-cloud-sdk /usr/local/google-cloud-sdk && \
    ln -s /usr/local/google-cloud-sdk/bin/gcloud /usr/local/bin/gcloud && \
    ln -s /usr/local/google-cloud-sdk/bin/gsutil /usr/local/bin/gsutil && \
    rm google-cloud-cli-linux-x86_64.tar.gz && \
    gcloud components install gke-gcloud-auth-plugin --quiet

# Tell kubectl to use the gke-gcloud-auth-plugin
ENV USE_GKE_GCLOUD_AUTH_PLUGIN=True

COPY --from=builder /app/mendel /usr/local/bin/mendel
COPY --from=builder /app/schema /schema
EXPOSE 8080
CMD ["mendel", "serve"]
