FROM registry.access.redhat.com/ubi10/go-toolset:1.26 AS builder
USER 0
WORKDIR /workspace

ENV GOCACHE=/workspace/.cache/go-build \
    GOTMPDIR=/workspace/.cache/go-tmp \
    GOMODCACHE=/workspace/.cache/go-mod

COPY go.mod go.sum ./
RUN mkdir -p /workspace/.cache/go-build /workspace/.cache/go-tmp /workspace/.cache/go-mod && \
    go mod download
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o manager cmd/main.go

FROM registry.access.redhat.com/ubi10-minimal:10.2
RUN microdnf install -y ca-certificates && microdnf clean all
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
