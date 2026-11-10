# The hub as a container image. Made from ./build.sh release (CI does this on
# every version tag and publishes ghcr.io/simplyph0/braid-hub).
# The hub fetches new signed PC builds from GitHub by itself (into /data), so
# the container rarely needs replacing.
FROM alpine:3 AS certs
RUN apk add --no-cache ca-certificates

FROM scratch
ARG TARGETARCH
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY dist/hub-linux-${TARGETARCH}/braid-hub /braid-hub
VOLUME /data
EXPOSE 8490
ENTRYPOINT ["/braid-hub", "-data", "/data"]
