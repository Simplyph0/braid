# The hub as a container image. Made from ./build.sh release (CI does this on
# every version tag and publishes ghcr.io/simplyph0/braid-hub).
FROM scratch
ARG TARGETARCH
COPY dist/hub-linux-${TARGETARCH}/braid-hub /braid-hub
COPY dist/update /update
VOLUME /data
EXPOSE 8490
ENTRYPOINT ["/braid-hub", "-data", "/data", "-updates", "/update"]
