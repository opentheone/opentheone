####################################
# Stage 1: build the Vue 3 frontend
####################################
FROM node:20-alpine AS frontend
WORKDIR /src/frontend

# Cache deps first so source-only changes don't bust the layer.
COPY frontend/package.json frontend/pnpm-lock.yaml* ./
# Don't use corepack: the version bundled with node:20-alpine ships a pnpm
# tarball that breaks at runtime with `ERR_UNKNOWN_BUILTIN_MODULE` on
# Node ≥ 20.20. Install pnpm directly to side-step that whole moving target;
# pin to pnpm@9 because our pnpm-lock.yaml is lockfileVersion 9.0 and a v10
# pnpm would silently rewrite it on `install --frozen-lockfile`.
RUN npm install -g pnpm@9 \
 && (pnpm install --frozen-lockfile || pnpm install)

COPY frontend/ ./
# vite is configured to emit into ../backend/internal/web/dist
RUN mkdir -p ../backend/internal/web/dist && pnpm build

####################################
# Stage 2: build the Go binary (CGO required for mattn/go-sqlite3)
####################################
FROM golang:1.25-alpine AS backend
RUN apk add --no-cache build-base git ca-certificates
WORKDIR /src

COPY backend/go.mod backend/go.sum* ./backend/
RUN cd backend && go mod download

COPY backend/ ./backend/
# pull in the freshly built frontend so go:embed sees real assets
COPY --from=frontend /src/backend/internal/web/dist ./backend/internal/web/dist

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

WORKDIR /src/backend
RUN CGO_ENABLED=1 GOOS=linux \
    go build \
      -trimpath \
      -ldflags="-s -w \
        -X main.Version=${VERSION} \
        -X main.Commit=${COMMIT} \
        -X main.BuildTime=${BUILD_TIME}" \
      -o /out/oto-server ./cmd/server

####################################
# Stage 3: minimal runtime image
####################################
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S oto && adduser -S oto -G oto

WORKDIR /app
COPY --from=backend /out/oto-server /usr/local/bin/oto-server
COPY backend/config.example.yaml /app/config.example.yaml

RUN mkdir -p /app/data /app/data/attachments && chown -R oto:oto /app
USER oto

VOLUME ["/app/data"]
EXPOSE 8080

ENV OTO_SERVER_LISTEN=":8080" \
    OTO_STORAGE_DATA_DIR="/app/data" \
    OTO_STORAGE_ATTACHMENTS_DIR="/app/data/attachments" \
    OTO_DATABASE_DRIVER="sqlite" \
    OTO_DATABASE_DSN="/app/data/oto.db"

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["oto-server"]
