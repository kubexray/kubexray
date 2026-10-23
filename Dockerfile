# syntax=docker/dockerfile:1

FROM node:24-alpine@sha256:06d7adf3e0ef61a13f1d28682726366f97a15aa1888c934d3ecec86c33918b53 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/ ./
COPY internal/web/dist/.keep ../internal/web/dist/.keep
RUN npm run build

FROM golang:1.27-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=web /src/internal/web/dist/ internal/web/dist/
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/kubexray/kubexray/internal/version.Version=${VERSION} -X github.com/kubexray/kubexray/internal/version.Commit=${COMMIT}" \
    -o /out/kubexray ./cmd/kubexray

# Distroless static, non-root (uid 65532): no shell, no package manager.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:d62067879de890a1def9da0e7766b7d20810a5e5555cef8506b752437d7a3e88
COPY --from=build /out/kubexray /kubexray
USER 65532:65532
ENTRYPOINT ["/kubexray"]
