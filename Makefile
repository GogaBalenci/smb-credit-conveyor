.PHONY: up up-ai down logs test test-unit test-integration test-frontend lint build fmt

TEST_DB ?= postgres://conveyor:conveyor@localhost:55432/conveyor_test?sslmode=disable

## Запустить весь стенд
up:
	docker compose up --build -d
	@echo "Веб: http://localhost:3000   Swagger: http://localhost:8080/api/docs"

## Стенд с ИИ-сводкой на локальной модели (Ollama + Qwen2.5 1.5B)
up-ai:
	docker compose -f docker-compose.yml -f docker-compose.ai.yml up --build -d
	@echo "Модель скачивается в фоне: docker compose -f docker-compose.yml -f docker-compose.ai.yml logs -f ollama-pull"

down:
	docker compose -f docker-compose.yml -f docker-compose.ai.yml down

logs:
	docker compose logs -f api worker

## Все тесты: unit + интеграционные (Postgres в Docker) + фронтенд
test: test-unit test-integration test-frontend

test-unit:
	cd backend && go test -race ./...

## Интеграционные и e2e-тесты на настоящем PostgreSQL
test-integration:
	docker run -d --rm --name conveyor-test-db -e POSTGRES_USER=conveyor -e POSTGRES_PASSWORD=conveyor \
		-e POSTGRES_DB=conveyor_test -p 55432:5432 postgres:16-alpine >/dev/null
	@until docker exec conveyor-test-db pg_isready -U conveyor >/dev/null 2>&1; do sleep 1; done
	cd backend && TEST_DATABASE_URL="$(TEST_DB)" go test -count=1 -p 1 ./... ; \
		status=$$?; docker stop conveyor-test-db >/dev/null; exit $$status

test-frontend:
	cd frontend && npm ci && npm test && npm run typecheck

lint:
	cd backend && gofmt -l . && go vet ./...

fmt:
	cd backend && gofmt -w .

build:
	cd backend && go build ./...
	cd frontend && npm run build
