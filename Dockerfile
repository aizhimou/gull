FROM golang:1.25-alpine AS build

WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o /out/vget-server ./cmd/vget-server

RUN mkdir -p /out/downloads

FROM debian:12-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        iputils-ping \
        procps \
        wget \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build /out/vget-server /app/vget-server
COPY --from=build /out/downloads /downloads

EXPOSE 8080

ENTRYPOINT ["/app/vget-server"]
CMD ["--port", "8080", "--output", "/downloads"]
