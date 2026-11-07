# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
# TARGETARCH is set by BuildKit/buildx; with the legacy builder it is empty and the native arch is used.
RUN if [ -n "$TARGETARCH" ]; then export GOARCH="$TARGETARCH"; fi; \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X github.com/KVM-X/KVM-X/internal/cli.Version=${VERSION} -X github.com/KVM-X/KVM-X/internal/cli.Commit=${COMMIT}" \
    -o /out/kvmx ./cmd/kvmx

FROM alpine:3.24
LABEL org.opencontainers.image.source="https://github.com/KVM-X/KVM-X" \
      org.opencontainers.image.description="KVM-X: detect, enable or fall back from KVM to tuned QEMU TCG" \
      org.opencontainers.image.licenses="MIT"
# QEMU for both guest architectures (software emulation works for either).
RUN apk add --no-cache qemu-system-x86_64 qemu-system-aarch64 qemu-img ca-certificates
COPY --from=build /out/kvmx /usr/local/bin/kvmx
# Cached test-guest downloads live here; mount a volume to keep them.
ENV KVMX_CACHE_DIR=/var/cache/kvmx
VOLUME /var/cache/kvmx
ENTRYPOINT ["kvmx"]
CMD ["status"]
