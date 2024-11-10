FROM alpine:3.20 AS builder
RUN apk update && apk add --no-cache ca-certificates tzdata && update-ca-certificates

# STEP 2 build a small image including module support
FROM alpine:3.20

ENV TZ=Europe/Berlin

# Import from builder
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY app /usr/local/bin/app

CMD [ "app" ]
