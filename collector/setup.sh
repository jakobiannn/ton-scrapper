#!/usr/bin/env bash
# =============================================================================
# setup.sh — единоразовый запуск всего стека TON Scrapper
#
# Что делает:
#   1. Проверяет зависимости (docker, docker-compose, go)
#   2. Создаёт .env из .env.example если нет
#   3. Поднимает TimescaleDB + Kafka + UI
#   4. Ждёт health-чеков всех сервисов
#   5. Опционально поднимает Python consumer
#   6. Выводит сводку эндпоинтов
#
# Использование:
#   chmod +x setup.sh
#   ./setup.sh              # только инфраструктура
#   ./setup.sh --consumer   # + Python consumer
#   ./setup.sh --all        # + Go scrapper в realtime режиме
# =============================================================================

set -euo pipefail

# ── Цвета ──────────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()     { echo -e "${CYAN}[setup]${NC} $*"; }
success() { echo -e "${GREEN}[✓]${NC} $*"; }
warn()    { echo -e "${YELLOW}[!]${NC} $*"; }
error()   { echo -e "${RED}[✗]${NC} $*" >&2; }
header()  { echo -e "\n${BOLD}$*${NC}"; }

# ── Флаги ─────────────────────────────────────────────────────────────────────
WITH_CONSUMER=false
WITH_SCRAPPER=false

for arg in "$@"; do
  case $arg in
    --consumer) WITH_CONSUMER=true ;;
    --all)      WITH_CONSUMER=true; WITH_SCRAPPER=true ;;
    --help|-h)
      echo "Использование: $0 [--consumer] [--all]"
      echo "  --consumer   Запустить Python consumer (Kafka → TimescaleDB)"
      echo "  --all        Запустить consumer + Go scrapper (realtime)"
      exit 0
      ;;
    *) warn "Неизвестный флаг: $arg (игнорируем)" ;;
  esac
done

# ═══════════════════════════════════════════════════════════════════════════════
header "1. Проверка зависимостей"
# ═══════════════════════════════════════════════════════════════════════════════

check_dep() {
  local cmd=$1 hint=$2
  if command -v "$cmd" &>/dev/null; then
    success "$cmd $(${cmd} --version 2>/dev/null | head -1 | tr -d '\n' || true)"
  else
    error "$cmd не найден. $hint"
    exit 1
  fi
}

check_dep docker    "Установи Docker: https://docs.docker.com/get-docker/"
check_dep go        "Установи Go: https://go.dev/dl/"

# docker compose v2 (плагин) или docker-compose v1
if docker compose version &>/dev/null 2>&1; then
  COMPOSE_CMD="docker compose"
  success "docker compose (v2 plugin)"
elif command -v docker-compose &>/dev/null; then
  COMPOSE_CMD="docker-compose"
  success "docker-compose (v1 standalone)"
else
  error "Нужен docker-compose или docker compose plugin"
  exit 1
fi

# Проверяем что Docker daemon запущен
if ! docker info &>/dev/null; then
  error "Docker daemon не запущен. Запусти Docker Desktop или 'sudo systemctl start docker'"
  exit 1
fi

# ═══════════════════════════════════════════════════════════════════════════════
header "2. Конфигурация (.env)"
# ═══════════════════════════════════════════════════════════════════════════════

if [[ ! -f .env ]]; then
  if [[ -f .env.example ]]; then
    cp .env.example .env
    success "Создан .env из .env.example"
    warn "Проверь .env — возможно нужно изменить POSTGRES_PASSWORD"
  else
    warn ".env.example не найден, создаём минимальный .env"
    cat > .env <<'EOF'
POSTGRES_DB=ton_metrics
POSTGRES_USER=ton
POSTGRES_PASSWORD=ton_secret
KAFKA_TOPIC_BLOCKS=ton.blocks
KAFKA_TOPIC_METRICS=ton.metrics
KAFKA_GROUP_ID=ton-ml-consumer
CONSUMER_BATCH_SIZE=100
CONSUMER_BATCH_TIMEOUT=5.0
EOF
    success ".env создан с дефолтными значениями"
  fi
else
  success ".env уже существует, используем его"
fi

# Загружаем переменные для использования в скрипте
set -a
# shellcheck disable=SC1091
source .env
set +a

# ═══════════════════════════════════════════════════════════════════════════════
header "3. Создание директорий"
# ═══════════════════════════════════════════════════════════════════════════════

mkdir -p scripts consumer
success "Директории scripts/ и consumer/ готовы"

# ═══════════════════════════════════════════════════════════════════════════════
header "4. Запуск инфраструктуры (TimescaleDB + Kafka)"
# ═══════════════════════════════════════════════════════════════════════════════

log "Поднимаем сервисы..."
$COMPOSE_CMD up -d zookeeper kafka kafka-ui timescaledb

# ── Ожидание TimescaleDB ───────────────────────────────────────────────────────
log "Ждём TimescaleDB..."
wait_for_postgres() {
  local max_attempts=30 attempt=0
  until $COMPOSE_CMD exec -T timescaledb \
      pg_isready -U "${POSTGRES_USER:-ton}" -d "${POSTGRES_DB:-ton_metrics}" \
      &>/dev/null; do
    attempt=$((attempt + 1))
    if [[ $attempt -ge $max_attempts ]]; then
      error "TimescaleDB не поднялся за ${max_attempts}×10s"
      $COMPOSE_CMD logs timescaledb | tail -20
      exit 1
    fi
    echo -n "."
    sleep 10
  done
  echo ""
}
wait_for_postgres
success "TimescaleDB готов"

# ── Ожидание Kafka ─────────────────────────────────────────────────────────────
log "Ждём Kafka..."
wait_for_kafka() {
  local max_attempts=20 attempt=0
  until $COMPOSE_CMD exec -T kafka \
      kafka-broker-api-versions --bootstrap-server localhost:9092 \
      &>/dev/null; do
    attempt=$((attempt + 1))
    if [[ $attempt -ge $max_attempts ]]; then
      error "Kafka не поднялась за ${max_attempts}×10s"
      $COMPOSE_CMD logs kafka | tail -20
      exit 1
    fi
    echo -n "."
    sleep 10
  done
  echo ""
}
wait_for_kafka
success "Kafka готова"

# ── Создание топиков ───────────────────────────────────────────────────────────
log "Создаём Kafka топики..."
TOPIC_BLOCKS="${KAFKA_TOPIC_BLOCKS:-ton.blocks}"
TOPIC_METRICS="${KAFKA_TOPIC_METRICS:-ton.metrics}"

for topic in "$TOPIC_BLOCKS" "$TOPIC_METRICS"; do
  $COMPOSE_CMD exec -T kafka \
    kafka-topics --bootstrap-server localhost:9092 \
    --create --if-not-exists \
    --topic "$topic" \
    --partitions 3 \
    --replication-factor 1 \
    --config retention.ms=604800000 \
    2>/dev/null && success "Топик '$topic' готов" || warn "Топик '$topic' уже существует"
done

# ═══════════════════════════════════════════════════════════════════════════════
header "5. Проверка схемы БД"
# ═══════════════════════════════════════════════════════════════════════════════

# Схема применяется автоматически через docker-entrypoint-initdb.d при первом старте.
# Проверяем что таблица существует.
TABLE_EXISTS=$($COMPOSE_CMD exec -T timescaledb \
  psql -U "${POSTGRES_USER:-ton}" -d "${POSTGRES_DB:-ton_metrics}" -tAc \
  "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='block_metrics');" \
  2>/dev/null | tr -d '[:space:]')

if [[ "$TABLE_EXISTS" == "t" ]]; then
  success "Схема БД применена (таблица block_metrics существует)"
else
  warn "Таблица block_metrics не найдена, применяем схему вручную..."
  if [[ -f scripts/init_schema.sql ]]; then
    $COMPOSE_CMD exec -T timescaledb \
      psql -U "${POSTGRES_USER:-ton}" -d "${POSTGRES_DB:-ton_metrics}" \
      -f /docker-entrypoint-initdb.d/01_schema.sql
    success "Схема применена вручную"
  else
    warn "scripts/init_schema.sql не найден — схема не применена"
    warn "Запусти: make db-schema"
  fi
fi

# ═══════════════════════════════════════════════════════════════════════════════
header "6. Python consumer"
# ═══════════════════════════════════════════════════════════════════════════════

if $WITH_CONSUMER; then
  if [[ ! -f consumer/Dockerfile ]]; then
    error "consumer/Dockerfile не найден. Создай его сначала."
    exit 1
  fi
  log "Собираем образ consumer..."
  $COMPOSE_CMD build ton-consumer
  log "Запускаем consumer..."
  $COMPOSE_CMD --profile consumer up -d ton-consumer
  success "Python consumer запущен"
else
  log "Пропускаем consumer (запусти с --consumer или: make consumer-up)"
fi

# ═══════════════════════════════════════════════════════════════════════════════
header "7. Go scrapper"
# ═══════════════════════════════════════════════════════════════════════════════

if $WITH_SCRAPPER; then
  log "Скачиваем Go-зависимости..."
  go mod download
  log "Запускаем Go scrapper в realtime режиме..."
  log "(Ctrl+C для остановки, логи в терминале)"
  KAFKA_ENABLED=true MODE=realtime DETAILED=true KAFKA_BROKERS=localhost:9092 go run .
else
  log "Пропускаем Go scrapper (запусти: make run-realtime)"
fi

# ═══════════════════════════════════════════════════════════════════════════════
header "✅  Стек готов"
# ═══════════════════════════════════════════════════════════════════════════════

echo ""
echo -e "  ${BOLD}Сервисы:${NC}"
echo -e "  ${GREEN}▸${NC} Kafka broker      ${CYAN}localhost:9092${NC}"
echo -e "  ${GREEN}▸${NC} Kafka UI          ${CYAN}http://localhost:8080${NC}"
echo -e "  ${GREEN}▸${NC} TimescaleDB       ${CYAN}localhost:5432${NC}  db=${POSTGRES_DB:-ton_metrics} user=${POSTGRES_USER:-ton}"
if $WITH_CONSUMER; then
  echo -e "  ${GREEN}▸${NC} Python consumer   ${CYAN}running${NC} (docker logs ton-consumer)"
fi
echo ""
echo -e "  ${BOLD}Следующие шаги:${NC}"
echo -e "  ${YELLOW}make run-realtime${NC}     — запустить Go scrapper"
echo -e "  ${YELLOW}make consumer-logs${NC}    — логи Python consumer"
echo -e "  ${YELLOW}make db-query${NC}         — открыть psql"
echo -e "  ${YELLOW}make status${NC}           — статус всех контейнеров"
echo ""