# Static controller binary on a distroless, non-root runtime. The runtime
# user is uid 65532, which the Deployment in config/manager relies on.
# Both base images are pinned by digest; bump the tag and digest together.
#
# The build stage runs on the builder's own platform and cross-compiles for
# the target, so a multi-platform build does not emulate the Go toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/zoneroute-controller ./cmd/zoneroute-controller

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
# Release metadata. Empty for a local `make build-image`; the release
# workflow passes the tag and commit. No created timestamp: it would change
# the image on every build without telling anyone anything the release does
# not already say.
ARG VERSION=""
ARG REVISION=""
LABEL org.opencontainers.image.title="zoneroute" \
      org.opencontainers.image.description="Kubernetes-native conditional DNS forwarding for CoreDNS" \
      org.opencontainers.image.source="https://github.com/mihnk/zoneroute" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /out/zoneroute-controller /zoneroute-controller
USER 65532:65532
ENTRYPOINT ["/zoneroute-controller"]
