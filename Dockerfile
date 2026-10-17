FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/expense-tracking-bot ./cmd/expense-tracking-bot

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /
COPY --from=builder /bin/expense-tracking-bot /bin/expense-tracking-bot
RUN addgroup -S app && adduser -S -G app app
USER app
EXPOSE 8080
ENTRYPOINT ["/bin/expense-tracking-bot"]
