FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/memoryd/ ./cmd/memoryd/
COPY cmd/memory-principal/ ./cmd/memory-principal/
COPY internal/memory/ ./internal/memory/
COPY internal/prices/ ./internal/prices/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /memoryd ./cmd/memoryd \
 && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /memory-principal ./cmd/memory-principal

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /memoryd /memoryd
COPY --from=build /memory-principal /memory-principal
USER 65532:65532
ENTRYPOINT ["/memoryd"]
CMD ["--listen", "0.0.0.0:8091"]
