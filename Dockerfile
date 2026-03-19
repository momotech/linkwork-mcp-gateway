FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o linkwork-mcp-gateway ./cmd/gateway

FROM alpine:3.19
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/linkwork-mcp-gateway .
COPY --from=builder /app/config.yaml .
EXPOSE 9080
ENTRYPOINT ["./linkwork-mcp-gateway"]
CMD ["-config", "config.yaml"]
