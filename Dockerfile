# Build manager + triage + fix binaries
FROM golang:1.26 AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o manager ./cmd/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o triage ./cmd/triage
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o fix ./cmd/fix

# Operator + triage image (distroless; no Go toolchain).
FROM gcr.io/distroless/static:nonroot AS operator
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=builder /workspace/triage .
USER 65532:65532

ENTRYPOINT ["/manager"]

# Fix Job image: needs Go toolchain for `go test` (decision T9/T10 #1).
FROM golang:1.26 AS fix
WORKDIR /
COPY --from=builder /workspace/fix /fix
USER 65532:65532
ENV HOME=/workspace \
    GOCACHE=/workspace/.cache \
    GOMODCACHE=/workspace/gomod \
    GOPATH=/workspace/go \
    TMPDIR=/workspace/tmp
ENTRYPOINT ["/fix"]
