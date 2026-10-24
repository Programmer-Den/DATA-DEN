# Builds one image for the machine it is pulled on (amd64 for the storage VM,
# arm64 for a Pi). The frontend and the Go binary are built on the build
# machine's own architecture and the binary is cross-compiled for the target.

FROM --platform=$BUILDPLATFORM node:26-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
# Writes the website into /src/backend/internal/web/files/build.
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS backend
ARG TARGETOS
ARG TARGETARCH
# The version the program reports: 1.2.3 for a release.
ARG VERSION=dev
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=frontend /src/backend/internal/web/files/build ./internal/web/files/build
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags="-s -w -X github.com/Programmer-Den/DATA-DEN/backend/internal/version.Version=$VERSION" \
      -o /out/data-den ./cmd/data-den

# A minimal image without a shell or package manager, running as a non-root user.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=backend /out/data-den /data-den
EXPOSE 8080
ENTRYPOINT ["/data-den"]
