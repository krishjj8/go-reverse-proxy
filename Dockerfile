FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o /app/proxy ./cmd/proxy/main.go



#Production

FROM gcr.io/distroless/static-debian12

WORKDIR /

COPY --from=builder /app/proxy /proxy
COPY --from=builder /app/config.yaml /config.yaml

EXPOSE 8080 9090

ENTRYPOINT ["/proxy"]