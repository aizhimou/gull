FROM golang:1.22-alpine AS build

WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o /out/vget-server ./cmd/vget-server

RUN mkdir -p /out/downloads

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=build /out/vget-server /app/vget-server
COPY --from=build --chown=65532:65532 /out/downloads /downloads

EXPOSE 8080

USER 65532:65532

ENTRYPOINT ["/app/vget-server"]
CMD ["--port", "8080", "--output", "/downloads"]
