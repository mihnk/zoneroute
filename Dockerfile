# Static controller binary on a distroless, non-root runtime. The runtime
# user is uid 65532, which the Deployment in config/manager relies on.
# Both base images are pinned by digest; bump the tag and digest together.
FROM golang:1.26.8-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zoneroute-controller ./cmd/zoneroute-controller

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/zoneroute-controller /zoneroute-controller
USER 65532:65532
ENTRYPOINT ["/zoneroute-controller"]
