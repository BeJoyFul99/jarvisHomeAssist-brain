FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o brain .

# Stage 2
FROM alpine:latest
RUN apk add --no-cache ca-certificates
ARG PORT
ENV PORT=${PORT}

WORKDIR /root/
COPY --from=builder /app/brain .

EXPOSE ${PORT}
CMD ["./brain"]