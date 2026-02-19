FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/mihomo-monitor .

FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /out/mihomo-monitor /usr/local/bin/mihomo-monitor

ENTRYPOINT ["mihomo-monitor"]
CMD ["--monitor"]
