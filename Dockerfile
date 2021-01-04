FROM golang:1.15 as builder
COPY . /app
WORKDIR /app
RUN CGO_ENABLED=0 go build -o terminator

FROM alpine
WORKDIR /app
RUN apk update && apk add ca-certificates && rm -rf /var/cache/apk/*
COPY  --from=builder /app/terminator .
