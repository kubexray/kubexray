# syntax=docker/dockerfile:1

FROM golang:1.27-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/kubexray/kubexray/internal/version.Version=${VERSION} -X github.com/kubexray/kubexray/internal/version.Commit=${COMMIT}" \
    -o /out/kubexray ./cmd/kubexray

# Distroless static, non-root (uid 65532): no shell, no package manager.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087
COPY --from=build /out/kubexray /kubexray
USER 65532:65532
ENTRYPOINT ["/kubexray"]
