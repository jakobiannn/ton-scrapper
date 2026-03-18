.PHONY: help build run kafka-up kafka-down kafka-logs kafka-ui deps tidy

help: ## Показать список команд
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ── Зависимости ──────────────────────────────────────────────────────────────

deps: ## Скачать зависимости
	go mod download

tidy: ## Обновить go.mod + go.sum
	go mod tidy

# ── Сборка и запуск ──────────────────────────────────────────────────────────

build: ## Собрать бинарник
	go build -o bin/ton-scrapper .

run-realtime: ## Запуск в режиме real-time (с Kafka)
	KAFKA_ENABLED=true MODE=realtime DETAILED=true go run .

run-historical: ## Запуск в режиме исторической загрузки
	KAFKA_ENABLED=true MODE=historical DETAILED=false WORKER_COUNT=5 go run .

run-both: ## Запуск в режиме historical → realtime
	KAFKA_ENABLED=true MODE=both DETAILED=true go run .

run-no-kafka: ## Запуск без Kafka (только логи)
	KAFKA_ENABLED=false MODE=realtime DETAILED=true go run .

# ── Kafka (Docker) ───────────────────────────────────────────────────────────

kafka-up: ## Поднять Kafka + Zookeeper + UI
	docker-compose up -d
	@echo "Ждём запуска Kafka..."
	@sleep 10
	@echo "✅  Kafka UI: http://localhost:8080"
	@echo "✅  Kafka:    localhost:9092"

kafka-down: ## Остановить Kafka контейнеры
	docker-compose down

kafka-clean: ## Остановить и удалить volumes (сброс данных)
	docker-compose down -v

kafka-logs: ## Логи Kafka
	docker-compose logs -f kafka

kafka-ui: ## Открыть Kafka UI в браузере
	open http://localhost:8080

kafka-topics: ## Показать существующие топики
	docker exec ton-kafka kafka-topics --bootstrap-server localhost:9092 --list

kafka-describe: ## Описание топиков ton.blocks и ton.metrics
	docker exec ton-kafka kafka-topics --bootstrap-server localhost:9092 \
		--describe --topic ton.blocks
	docker exec ton-kafka kafka-topics --bootstrap-server localhost:9092 \
		--describe --topic ton.metrics

kafka-consume: ## Читать сообщения из ton.blocks в консоли
	docker exec -it ton-kafka kafka-console-consumer \
		--bootstrap-server localhost:9092 \
		--topic ton.blocks \
		--from-beginning \
		--max-messages 10

# ── Тесты ─────────────────────────────────────────────────────────────────────

test: ## Запустить тесты
	go test ./...

vet: ## Проверить код
	go vet ./...
