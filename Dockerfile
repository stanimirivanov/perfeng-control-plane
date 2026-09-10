FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/control-plane ./cmd/control-plane && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/validate-registry ./cmd/validate-registry

FROM gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7

ARG VCS_REF=unknown
LABEL org.opencontainers.image.source="https://github.com/stanimirivanov/perfeng-control-plane" \
      org.opencontainers.image.revision="$VCS_REF" \
      org.opencontainers.image.title="perfeng-control-plane" \
      org.opencontainers.image.licenses="Apache-2.0"

ENV HOME=/nonexistent
WORKDIR /var/empty
COPY --from=builder --chown=65532:65532 /out/ /usr/local/bin/
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/control-plane"]
