FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY skydata ./skydata
COPY editor ./editor
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /deskpanel .

# TrueNAS is pinned by fingerprint and the rest of the LAN is plain HTTP, but weather calls public
# HTTPS (NWS, IEM radar, OpenStreetMap, Open-Meteo), so the image carries the build stage's CA bundle.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /deskpanel /deskpanel
EXPOSE 8088
ENTRYPOINT ["/deskpanel"]
CMD ["serve"]
