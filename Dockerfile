# Всё-в-одном образ для бесплатного хостинга (Render, Railway, Koyeb и т.п.):
# собирает фронтенд и запускает API + воркер в одном процессе, раздавая SPA.
# Нужен внешний PostgreSQL (DATABASE_URL) и симулятор источников (SOURCES_URL) —
# либо второй сервис из того же репозитория с target=mockgov (backend/Dockerfile).

FROM node:22-alpine AS web
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download && go mod verify
COPY backend/ .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/conveyor ./cmd/conveyor

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/conveyor /conveyor
COPY --from=web /app/dist /web
ENV STATIC_DIR=/web PORT=8080
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s CMD ["/conveyor", "healthcheck"]
ENTRYPOINT ["/conveyor"]
CMD ["all"]
