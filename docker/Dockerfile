FROM golang:1.19-alpine3.18 AS builder

WORKDIR /app

RUN apk update \
  && apk add --virtual build-deps gcc git \
  && rm -rf /var/cache/apk/*

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod/ \
  go install -v ./cmd/...

FROM alpine:3.18

COPY --from=builder /go/bin/ /app/

WORKDIR /app

EXPOSE 8080
ENTRYPOINT ["./rp-archiver"]
