FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/backend .

FROM alpine:3.20

RUN adduser -D appuser && mkdir -p /app-data && chown appuser:appuser /app-data
COPY --from=builder /out/backend /usr/local/bin/backend

WORKDIR /app-data

USER appuser

EXPOSE 8080

CMD ["backend"]
