FROM node:24-bookworm-slim AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /platform ./cmd/platform

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=backend /platform /app/platform
COPY --from=web /web/dist /app/web/dist
ENV WEB_DIR=/app/web/dist
ENTRYPOINT ["/app/platform"]
CMD ["api"]
