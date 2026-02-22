# Build the binaries
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build all three components
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o bin/distort-manager cmd/distort-manager/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o bin/distort-agent cmd/distort-agent/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o bin/distort-csi cmd/distort-csi/main.go

# Use ubuntu as base image for the final stages since the agent needs
# access to system binaries like parted and nvme-cli.
FROM ubuntu:24.04
WORKDIR /
COPY --from=builder /workspace/bin/distort-manager /usr/local/bin/
COPY --from=builder /workspace/bin/distort-agent /usr/local/bin/
COPY --from=builder /workspace/bin/distort-csi /usr/local/bin/

# Install runtime dependencies required by the agent and CSI
RUN apt-get update && apt-get install -y nvme-cli parted e2fsprogs xfsprogs kmod && rm -rf /var/lib/apt/lists/*

# Manager should run non-privileged but agent/csi need root
USER 0:0

ENTRYPOINT ["/usr/local/bin/distort-manager"]
