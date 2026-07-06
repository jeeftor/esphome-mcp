FROM --platform=$BUILDPLATFORM alpine:3.20 AS certs

RUN apk --no-cache add ca-certificates

FROM scratch

COPY esphome-mcp /usr/local/bin/esphome-mcp
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

USER 65532:65532
EXPOSE 3333
ENTRYPOINT ["/usr/local/bin/esphome-mcp"]
CMD ["serve", "--http-addr", "0.0.0.0:3333"]
