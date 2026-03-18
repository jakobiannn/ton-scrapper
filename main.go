package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ton-scrapper/collector"
	"ton-scrapper/config"
	"ton-scrapper/models"
)

func main() {
	log.Println("=== TON Scrapper запуск ===")

	cfg := config.Load()
	log.Printf("Режим: %s | Воркеров: %d | Detailed: %v | Kafka: %v",
		cfg.Mode, cfg.WorkerCount, cfg.Detailed, cfg.Kafka.Enabled)

	// --- Контекст с отменой ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Сигналы остановки ---
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// --- Подключение к TON ---
	client, err := collector.NewTONClient("https://ton.org/global.config.json")
	if err != nil {
		log.Fatalf("Ошибка подключения к TON: %v", err)
	}

	// --- Kafka продюсер (опционально) ---
	var producer *collector.KafkaProducer
	if cfg.Kafka.Enabled {
		log.Printf("Подключение к Kafka: %v", cfg.Kafka.Brokers)

		// Создаём топики если нет
		topics := collector.DefaultTopics(cfg.Kafka.TopicBlocks, cfg.Kafka.TopicMetrics)
		if err := collector.EnsureTopics(cfg.Kafka.Brokers, topics); err != nil {
			log.Printf("Предупреждение: не удалось создать топики Kafka: %v", err)
			log.Println("Продолжаем работу без Kafka (метрики будут только в лог)")
			cfg.Kafka.Enabled = false
		} else {
			producer = collector.NewKafkaProducer(cfg.Kafka.Brokers, cfg.Kafka.TopicBlocks)
			defer producer.Close()
			log.Printf("Kafka подключена: топик=%s", cfg.Kafka.TopicBlocks)
		}
	}

	// --- Канал метрик (буфер 2000 блоков) ---
	metricsChan := make(chan *models.BlockMetrics, 2000)

	// --- Горутина публикации в Kafka / логирования ---
	go func() {
		var count int64
		var kafkaErrors int64

		for metrics := range metricsChan {
			count++

			// Публикуем в Kafka
			if producer != nil {
				if err := producer.PublishBlockMetrics(ctx, metrics); err != nil {
					kafkaErrors++
					log.Printf("Kafka ошибка публикации блока %d: %v", metrics.SeqNo, err)
				}
			}

			// Логируем каждые 50 блоков
			if count%50 == 0 {
				log.Printf("[Метрики] Обработано блоков: %d | Kafka ошибок: %d | Последний: seqno=%d tx=%d addr=%d blockTime=%.2fs",
					count, kafkaErrors,
					metrics.SeqNo, metrics.TransactionCount, metrics.UniqueAddresses, metrics.BlockTime)
			}
		}
		log.Printf("Обработчик метрик завершён. Итого блоков: %d", count)
	}()

	// --- Запуск в зависимости от режима ---
	switch cfg.Mode {
	case "realtime":
		log.Println("Режим: Real-time стриминг")
		processor := collector.NewTonStreamProcessor(client)

		go func() {
			if err := processor.SubscribeToBlocks(ctx, metricsChan, cfg.Detailed); err != nil {
				if ctx.Err() == nil {
					log.Printf("Ошибка стриминга: %v", err)
				}
			}
		}()

	case "historical":
		log.Println("Режим: Загрузка исторических данных")
		loader := collector.NewHistoricalLoader(client, cfg.WorkerCount)

		api := client.GetAPI()
		current, err := api.CurrentMasterchainInfo(ctx)
		if err != nil {
			log.Fatalf("Не удалось получить текущий блок: %v", err)
		}

		// По умолчанию — последние 10 000 блоков (~14 часов)
		startSeqno := current.SeqNo - 10_000
		log.Printf("Загрузка блоков %d — %d", startSeqno, current.SeqNo)

		go func() {
			if err := loader.LoadHistoricalBlocks(ctx, startSeqno, current.SeqNo, metricsChan, cfg.Detailed); err != nil {
				if ctx.Err() == nil {
					log.Printf("Ошибка загрузки: %v", err)
				}
			}
			log.Println("Историческая загрузка завершена, остановка...")
			cancel()
		}()

	case "both":
		log.Println("Режим: Исторические данные → Real-time (sequential)")
		loader := collector.NewHistoricalLoader(client, cfg.WorkerCount)
		processor := collector.NewTonStreamProcessor(client)

		api := client.GetAPI()
		current, err := api.CurrentMasterchainInfo(ctx)
		if err != nil {
			log.Fatalf("Не удалось получить текущий блок: %v", err)
		}

		startSeqno := current.SeqNo - 10_000
		log.Printf("Шаг 1: историческая загрузка блоков %d — %d", startSeqno, current.SeqNo)

		go func() {
			// Сначала — история
			if err := loader.LoadHistoricalBlocks(ctx, startSeqno, current.SeqNo, metricsChan, cfg.Detailed); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("Ошибка исторической загрузки: %v", err)
			}

			// Затем — real-time
			log.Println("Шаг 2: переключение в real-time режим")
			if err := processor.SubscribeToBlocks(ctx, metricsChan, cfg.Detailed); err != nil {
				if ctx.Err() == nil {
					log.Printf("Ошибка real-time стриминга: %v", err)
				}
			}
		}()

	default:
		log.Fatalf("Неизвестный режим: %q (допустимые: realtime, historical, both)", cfg.Mode)
	}

	// --- Ожидание сигнала остановки ---
	sig := <-sigChan
	log.Printf("Получен сигнал %v, останавливаемся...", sig)

	cancel()

	// Даём горутинам время завершить работу
	time.Sleep(2 * time.Second)
	close(metricsChan)

	log.Println("=== TON Scrapper остановлен ===")
}
