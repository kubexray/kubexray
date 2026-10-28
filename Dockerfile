# syntax=docker/dockerfile:1

# The UI and the binary build on the build machine, whatever the target: the
# UI is the same for every platform, and Go cross-compiles. Only the last
# stage is per platform, and it only copies, so no emulation is needed.
FROM --platform=$BUILDPLATFORM node:24-alpine@sha256:06d7adf3e0ef61a13f1d28682726366f97a15aa1888c934d3ecec86c33918b53 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/ ./
COPY internal/web/dist/.keep ../internal/web/dist/.keep
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=web /src/internal/web/dist/ internal/web/dist/
ARG VERSION=dev
ARG COMMIT=none
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/kubexray/kubexray/internal/version.Version=${VERSION} -X github.com/kubexray/kubexray/internal/version.Commit=${COMMIT}" \
    -o /out/kubexray ./cmd/kubexray

# Distroless static, non-root (uid 65532): no shell, no package manager.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/kubexray /kubexray
USER 65532:65532
ENTRYPOINT ["/kubexray"]
