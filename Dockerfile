# The hub as a container (Podman in Cockpit: Containers → Create from this image).
FROM golang:1.27 AS build
WORKDIR /src
COPY . .
RUN ./build.sh
FROM scratch
COPY --from=build /src/dist/braid-hub /braid-hub
VOLUME /data
EXPOSE 8490
ENTRYPOINT ["/braid-hub", "-data", "/data"]
