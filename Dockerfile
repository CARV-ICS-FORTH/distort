# Create a builder stage specifically for SPDK to avoid bloating the final image
FROM ubuntu:24.04 AS spdk-builder

# Install dependencies required to build SPDK and RDMA components
ENV DEBIAN_FRONTEND=noninteractive
RUN sed -i 's/Components: main$/Components: main universe restricted multiverse/g' /etc/apt/sources.list.d/ubuntu.sources && \
    apt-get update && apt-get install -y \
    gcc g++ make pkg-config libnuma-dev python3 uuid-dev git \
    libibverbs-dev librdmacm-dev python3-pyelftools python3-venv \
    libcunit1-dev libaio-dev libssl-dev libjson-c-dev libcmocka-dev \
    libiscsi-dev libkeyutils-dev python3-pip python3-dev \
    unzip libfuse3-dev patchelf curl nasm yasm autoconf libtool \
    meson ninja-build help2man libncurses5-dev libncursesw5-dev \
    && rm -rf /var/lib/apt/lists/*

# Clone SPDK
WORKDIR /src
RUN git clone -b v26.01 https://github.com/spdk/spdk.git /src/spdk

WORKDIR /src/spdk
RUN git submodule update --init

# Build DPDK and SPDK with RDMA enabled
    RUN ./configure --with-rdma --disable-tests --disable-unit-tests || (cat /src/spdk/.spdk-isal.log && exit 1)
    RUN make -j$(nproc)


# Build the Go binaries
FROM golang:1.25 AS go-builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the Go source
COPY . .

# Build all three components
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o bin/distort-manager cmd/distort-manager/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o bin/distort-agent cmd/distort-agent/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o bin/distort-csi cmd/distort-csi/main.go


# Use ubuntu as base image for the final stages since the agent needs
# access to system binaries like parted and nvme-cli, and now SPDK dynamically linked libs
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive

WORKDIR /

# Copy Distort Go Binaries
COPY --from=go-builder /workspace/bin/distort-manager /usr/local/bin/
COPY --from=go-builder /workspace/bin/distort-agent /usr/local/bin/
COPY --from=go-builder /workspace/bin/distort-csi /usr/local/bin/

# Copy compiled SPDK Binaries & Scripts
# nvmf_tgt is the main SPDK Target executable
COPY --from=spdk-builder /src/spdk/build/bin/nvmf_tgt /usr/local/bin/nvmf_tgt
COPY --from=spdk-builder /src/spdk/scripts /opt/spdk/scripts
COPY --from=spdk-builder /src/spdk/python /opt/spdk/python
COPY --from=spdk-builder /src/spdk/include /opt/spdk/include

ENV PYTHONPATH=/opt/spdk/python


# Install runtime dependencies required by the agent, CSI, and SPDK RDMA
RUN apt-get update && apt-get install -y \
    nvme-cli parted e2fsprogs xfsprogs kmod \
    python3 libnuma1 libibverbs1 librdmacm1 pciutils \
    libfuse3-3 libaio1t64 \
    && rm -rf /var/lib/apt/lists/*

# Manager should run non-privileged but agent/csi need root
USER 0:0

ENTRYPOINT ["/usr/local/bin/distort-manager"]
