FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /storefront ./cmd/storefront

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /storefront /app/storefront
WORKDIR /app
ENTRYPOINT ["/app/storefront"]
CMD ["api"]
