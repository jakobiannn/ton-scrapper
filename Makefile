.PHONY: help setup build run-realtime run-historical run-both run-no-kafka \
        infra-up infra-down infra-clean infra-logs status \
        kafka-topics kafka-describe kafka-consume \
        consumer-up consumer-down consumer-logs consumer-build \
        db-query db-schema db-clean \
        test vet tidy deps

# Автоопределение docker compose v1 vs v2
COMPOSE := $(shell docker compose version > /dev/null 2>&1 && echo "docker compose" || echo "docker-compose")

# ── Справка ───────────────────────────────────────────────────────────────────

help: ## Показать список команд
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ── Первый запуск ─────────────────────────────────────────────────────────────

setup: ## Первый запуск: поднять весь стек (Kafka + TimescaleDB)
	@chmod +x setup.sh && ./setup.sh

setup-all: ## Первый запуск + consumer + scrapper
	@chmod +x setup.sh && ./setup.sh --all

# ── Зависимости ───────────────────────────────────────────────────────────────

deps: ## Скачать Go-зависимости
	go mod download

tidy: ## Обновить go.mod + go.sum
	go mod tidy

# ── Сборка и запуск Go scrapper ───────────────────────────────────────────────

build: ## Собрать бинарник
	go build -o bin/ton-scrapper .

run-realtime: ## Запуск в режиме real-time (с Kafka)
	KAFKA_ENABLED=true MODE=realtime DETAILED=true KAFKA_BROKERS=localhost:9092 go run .

run-historical: ## Запуск в режиме исторической загрузки
	KAFKA_ENABLED=true MODE=historical DETAILED=false WORKER_COUNT=5 KAFKA_BROKERS=localhost:9092 go run .

run-both: ## Запуск в режиме historical → realtime
	KAFKA_ENABLED=true MODE=both DETAILED=true KAFKA_BROKERS=localhost:9092 go run .

run-no-kafka: ## Запуск без Kafka (только логи)
	KAFKA_ENABLED=false MODE=realtime DETAILED=true go run .

# ── Инфраструктура (Kafka + TimescaleDB) ─────────────────────────────────────

infra-up: ## Поднять Kafka + ZooKeeper + UI + TimescaleDB
	$(COMPOSE) up -d zookeeper kafka kafka-ui timescaledb
	@echo ""
	@echo "  Kafka UI:    http://localhost:8080"
	@echo "  Kafka:       localhost:9092"
	@echo "  TimescaleDB: localhost:5432"

infra-down: ## Остановить инфраструктуру (данные сохраняются)
	$(COMPOSE) down

infra-clean: ## Остановить и удалить все данные (полный сброс)
	$(COMPOSE) down -v
	@echo "Все volumes удалены"

infra-logs: ## Логи всей инфраструктуры
	$(COMPOSE) logs -f kafka timescaledb

status: ## Статус всех контейнеров
	$(COMPOSE) ps

# ── Kafka ─────────────────────────────────────────────────────────────────────

kafka-up: ## Поднять только Kafka
	$(COMPOSE) up -d zookeeper kafka kafka-ui

kafka-down: ## Остановить только Kafka
	$(COMPOSE) stop kafka kafka-ui zookeeper

kafka-topics: ## Список топиков
	$(COMPOSE) exec kafka kafka-topics --bootstrap-server localhost:9092 --list

kafka-describe: ## Описание топика ton.blocks
	$(COMPOSE) exec kafka kafka-topics --bootstrap-server localhost:9092 \
		--describe --topic ton.blocks

kafka-consume: ## Читать 10 последних сообщений из ton.blocks
	$(COMPOSE) exec kafka kafka-console-consumer \
		--bootstrap-server localhost:9092 \
		--topic ton.blocks \
		--from-beginning \
		--max-messages 10

kafka-ui: ## Открыть Kafka UI в браузере
	open http://localhost:8080 || xdg-open http://localhost:8080

# ── Python consumer ───────────────────────────────────────────────────────────

consumer-build: ## Собрать Docker-образ consumer
	$(COMPOSE) build ton-consumer

consumer-up: ## Запустить Python consumer
	$(COMPOSE) --profile consumer up -d ton-consumer

consumer-down: ## Остановить Python consumer
	$(COMPOSE) stop ton-consumer

consumer-logs: ## Логи Python consumer (follow)
	$(COMPOSE) logs -f ton-consumer

consumer-restart: ## Перезапустить Python consumer
	$(COMPOSE) restart ton-consumer

# ── TimescaleDB ───────────────────────────────────────────────────────────────

db-query: ## Открыть psql в контейнере
	$(COMPOSE) exec timescaledb psql -U $${POSTGRES_USER:-ton} -d $${POSTGRES_DB:-ton_metrics}

db-schema: ## Применить схему вручную (если не применилась при старте)
	$(COMPOSE) exec -T timescaledb \
		psql -U $${POSTGRES_USER:-ton} -d $${POSTGRES_DB:-ton_metrics} \
		-f /docker-entrypoint-initdb.d/01_schema.sql

db-stats: ## Статистика таблиц TimescaleDB
	$(COMPOSE) exec timescaledb psql -U $${POSTGRES_USER:-ton} -d $${POSTGRES_DB:-ton_metrics} -c \
		"SELECT hypertable_name, num_chunks, \
		        pg_size_pretty(hypertable_size(hypertable_name::regclass)) AS size \
		 FROM timescaledb_information.hypertables;"

db-count: ## Сколько блоков в БД
	$(COMPOSE) exec timescaledb psql -U $${POSTGRES_USER:-ton} -d $${POSTGRES_DB:-ton_metrics} -c \
		"SELECT COUNT(*), MIN(seqno), MAX(seqno), MIN(timestamp), MAX(timestamp) \
		 FROM block_metrics;"

db-clean: ## Удалить все данные из block_metrics (оставить схему)
	$(COMPOSE) exec timescaledb psql -U $${POSTGRES_USER:-ton} -d $${POSTGRES_DB:-ton_metrics} -c \
		"TRUNCATE block_metrics;"

# ── Тесты ─────────────────────────────────────────────────────────────────────

test: ## Запустить все тесты
	go test ./... -v

test-short: ## Запустить тесты без -v
	go test ./...

vet: ## Статический анализ Go
	go vet ./...

# ── ML pipeline ───────────────────────────────────────────────────────────────

ml-install: ## Установить Python зависимости для ML
	pip install -r ml/requirements.txt

ml-features: ## Построить feature matrix (последние 24 часа)
	python -m ml.features.builder --hours 24 --info

ml-features-save: ## Сохранить feature matrix в parquet
	mkdir -p ml/data
	python -m ml.features.builder --hours 24 --output ml/data/features.parquet

ml-detect: ## Запустить все детекторы
	python -m ml.evaluate

ml-clean: ## Удалить сохранённые данные
	rm -rf ml/data/