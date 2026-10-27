FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /api-gateway .

# só o binário estático: sem shell nem pacotes para atacar
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /api-gateway /api-gateway
EXPOSE 8080
ENTRYPOINT ["/api-gateway"]
