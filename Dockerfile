FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG BUILD_TIME=0
RUN CGO_ENABLED=0 go build -ldflags="-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}" -o mendel ./cmd/mendel

FROM alpine:3.19
RUN apk add --no-cache ca-certificates git docker-cli docker-cli-compose
COPY --from=builder /app/mendel /usr/local/bin/mendel
COPY --from=builder /app/schema /schema
EXPOSE 8080
CMD ["mendel", "serve"]
