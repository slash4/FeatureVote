# syntax=docker/dockerfile:1

# 1. Widget bundle -> internal/web/static/widget.js
FROM node:22-alpine AS widget
WORKDIR /src/widget
COPY widget/package.json widget/package-lock.json ./
RUN npm ci
COPY widget/ ./
RUN mkdir -p /src/internal/web/static && npm run build

# 2. Go binaries (the widget is embedded via go:embed)
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=widget /src/internal/web/static/widget.js internal/web/static/widget.js
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/featurevote ./cmd/featurevote \
 && go build -trimpath -ldflags="-s -w" -o /out/demohost ./cmd/demohost \
 && go build -trimpath -ldflags="-s -w" -o /out/fvtoken ./cmd/fvtoken

# Demo host (local development only; see cmd/demohost). Build with --target demohost.
FROM gcr.io/distroless/static:nonroot AS demohost
COPY --from=build /out/demohost /demohost
COPY examples/ /examples/
EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/demohost", "-dir", "/examples"]

# 3. Runtime (default target). distroless has no shell/curl, so there is no
# Docker HEALTHCHECK: probe GET /healthz from the orchestrator instead.
FROM gcr.io/distroless/static:nonroot AS runtime
COPY --from=build /out/featurevote /featurevote
COPY --from=build /out/fvtoken /fvtoken
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/featurevote"]
