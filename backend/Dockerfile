# Сборка Go-сервисов: conveyor (API + воркер) и mockgov (симулятор внешних источников).
# Финальные образы — distroless/static: без shell и пакетного менеджера, запуск не от root.

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/conveyor ./cmd/conveyor \
 && go build -trimpath -ldflags="-s -w" -o /out/mockgov ./cmd/mockgov

FROM gcr.io/distroless/static-debian12:nonroot AS conveyor
COPY --from=build /out/conveyor /conveyor
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=5 CMD ["/conveyor", "healthcheck"]
ENTRYPOINT ["/conveyor"]
CMD ["all"]

FROM gcr.io/distroless/static-debian12:nonroot AS mockgov
COPY --from=build /out/mockgov /mockgov
USER nonroot:nonroot
EXPOSE 8090
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD ["/mockgov", "healthcheck"]
ENTRYPOINT ["/mockgov"]
