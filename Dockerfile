#############################################
# Stage 1: SPDK Builder (Rocky 9)
#############################################
FROM rockylinux:9 AS spdk-builder

ENV DEBIAN_FRONTEND=noninteractive

RUN dnf -y install dnf-plugins-core && \
    dnf config-manager --set-enabled crb && \
    dnf -y install epel-release && \
    dnf -y update && \
    dnf -y install \
        CUnit \
        gcc \
        gcc-c++ \
        make \
        pkgconf \
        pkgconf-pkg-config \
        numactl-devel \
        python3 \
        python3-devel \
        python3-pip \
        git \
        rdma-core-devel \
        libuuid-devel \
        libaio-devel \
        openssl-devel \
        json-c-devel \
        CUnit-devel \
        keyutils-libs-devel \
        fuse3-devel \
        nasm \
        autoconf \
        automake \
        libtool \
        meson \
        ninja-build \
        ncurses-devel \
        which \
        diffutils \
        patchelf \
    && dnf clean all


#############################################
# Clone SPDK
#############################################
WORKDIR /src

RUN git clone https://github.com/CARV-ICS-FORTH/nvmeof-bxi.git /src/spdk


#############################################
# Install BXI headers + libraries
#############################################

# IMPORTANT:
# host uses /usr/local/include first
RUN mkdir -p /usr/local/include/linux/bxi3

COPY bxilib/portals4.h /usr/local/include/
COPY bxilib/portals4_bxiext.h /usr/local/include/
COPY bxilib/bxi3/ /usr/local/include/linux/bxi3/


COPY bxilib/libportals-bxi3.so /lib64/

RUN ln -sf /lib64/libportals-bxi3.so /lib64/libportals.so

RUN echo "/lib64" > /etc/ld.so.conf.d/bxi.conf && \
    ldconfig


#############################################
# Verify BXI ABI
#############################################

RUN cat >/tmp/test_ioctl.c <<'EOF'
#include <stdio.h>
#include <sys/ioctl.h>
#include <linux/bxi3/bxi3_ioctl.h>

int main()
{
    printf("GET_INFO=0x%lx\n",
           (unsigned long)BXI3_IOCTL_GET_INFO);

    printf("sizeof(bxi3_info)=%lu\n",
           sizeof(struct bxi3_info));

    return 0;
}
EOF

RUN gcc /tmp/test_ioctl.c -o /tmp/test_ioctl && \
    /tmp/test_ioctl


#############################################
# Init submodules
#############################################

WORKDIR /src/spdk

RUN git -c safe.directory="*" submodule update --init


RUN python3 -m pip install --no-cache-dir --upgrade pip && \
    python3 -m pip install --no-cache-dir \
        meson==0.63.* \
        ninja==1.10.* \
        pyelftools \
        scan-build


#############################################
# Build DPDK
#############################################

WORKDIR /src/spdk/dpdk

RUN meson setup build \
    -Denable_libs=hash,eal,kvargs,log,ring,mempool,mbuf \
    -Denable_drivers="" \
    -Ddisable_drivers=net/gve


RUN ninja -C build


#############################################
# Build SPDK
#############################################

WORKDIR /src/spdk


RUN ./configure \
    --with-rdma=portals \
    --with-dpdk=./dpdk/build \
    --disable-tests \
    --disable-unit-tests


RUN make -j$(nproc)



#############################################
# Stage 2: Go Builder
#############################################

FROM golang:1.25 AS go-builder


WORKDIR /workspace


COPY go.mod go.sum ./

RUN go mod download


COPY . .


RUN CGO_ENABLED=0 go build \
    -o bin/distort-manager \
    cmd/distort-manager/main.go && \
    CGO_ENABLED=0 go build \
    -o bin/distort-agent \
    cmd/distort-agent/main.go && \
    CGO_ENABLED=0 go build \
    -o bin/distort-csi \
    cmd/distort-csi/main.go



#############################################
# Stage 3: Runtime
#############################################

FROM rockylinux:9


WORKDIR /


RUN dnf -y install \
    CUnit\
    nvme-cli \
    parted \
    e2fsprogs \
    xfsprogs \
    kmod \
    python3 \
    numactl-libs \
    rdma-core \
    libibverbs \
    librdmacm \
    pciutils \
    fuse3-libs \
    libaio \
    openssl-libs \
    json-c \
    procps-ng \
    && dnf clean all



#############################################
# Copy Go binaries
#############################################

COPY --from=go-builder \
    /workspace/bin/distort-manager \
    /usr/local/bin/


COPY --from=go-builder \
    /workspace/bin/distort-agent \
    /usr/local/bin/


COPY --from=go-builder \
    /workspace/bin/distort-csi \
    /usr/local/bin/



#############################################
# Copy SPDK
#############################################

COPY --from=spdk-builder \
    /src/spdk \
    /spdk



#############################################
# Copy BXI runtime
#############################################

COPY --from=spdk-builder \
    /lib64/libportals-bxi3.so \
    /lib64/

COPY --from=spdk-builder \
    /src/spdk/lib/rdma_provider \
    /opt/spdk/lib/rdma_provider/


RUN ln -sf /lib64/libportals-bxi3.so /lib64/libportals.so


RUN md5sum /lib64/libportals-bxi3.so

#############################################
# Dynamic linker config
#############################################

RUN echo "/opt/spdk/lib" > /etc/ld.so.conf.d/spdk.conf && \
    echo "/opt/spdk/lib/rdma_provider" >> /etc/ld.so.conf.d/spdk.conf && \
    echo "/lib64" > /etc/ld.so.conf.d/bxi.conf && \
    ldconfig



#############################################
# Environment
#############################################

ENV PYTHONPATH=/spdk/python

# Root of the SPDK tree copied above (/src/spdk -> /spdk). The agent derives
# scripts/rpc.py, scripts/setup.sh and build/bin/nvmf_tgt from this.
ENV SPDK_DIR=/spdk

ENV LD_LIBRARY_PATH=/opt/spdk/lib/rdma_provider:/opt/spdk/lib:/lib64


RUN md5sum /lib64/libportals-bxi3.so

ENTRYPOINT ["/usr/local/bin/distort-manager"]