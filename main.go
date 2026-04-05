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
	"ton-scrapper/collector/models"
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
		loader.SetCheckpointFile(cfg.CheckpointFile)

		api := client.GetAPI()
		current, err := api.CurrentMasterchainInfo(ctx)
		if err != nil {
			log.Fatalf("Не удалось получить текущий блок: %v", err)
		}

		// Определяем диапазон загрузки
		var startSeqno, endSeqno uint32
		if cfg.StartSeqNo > 0 {
			startSeqno = cfg.StartSeqNo
		} else {
			startSeqno = current.SeqNo - cfg.HistoryDepth
		}
		if cfg.EndSeqNo > 0 {
			endSeqno = cfg.EndSeqNo
		} else {
			endSeqno = current.SeqNo
		}

		// Checkpoint: возобновление с последней позиции
		if cp, err := loader.LoadCheckpoint(); err == nil && cp > startSeqno {
			log.Printf("Checkpoint найден: возобновление с блока %d (вместо %d)", cp+1, startSeqno)
			startSeqno = cp + 1
		}

		log.Printf("Загрузка блоков %d — %d (%d блоков)", startSeqno, endSeqno, endSeqno-startSeqno+1)

		go func() {
			if err := loader.LoadHistoricalBlocks(ctx, startSeqno, endSeqno, metricsChan, cfg.Detailed); err != nil {
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
		loader.SetCheckpointFile(cfg.CheckpointFile)
		processor := collector.NewTonStreamProcessor(client)

		api := client.GetAPI()
		current, err := api.CurrentMasterchainInfo(ctx)
		if err != nil {
			log.Fatalf("Не удалось получить текущий блок: %v", err)
		}

		var startSeqno uint32
		if cfg.StartSeqNo > 0 {
			startSeqno = cfg.StartSeqNo
		} else {
			startSeqno = current.SeqNo - cfg.HistoryDepth
		}

		if cp, err := loader.LoadCheckpoint(); err == nil && cp > startSeqno {
			log.Printf("Checkpoint: возобновление с блока %d", cp+1)
			startSeqno = cp + 1
		}

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

	case "toncenter":
		log.Println("Режим: Загрузка исторических данных через TonCenter API")
		loader := collector.NewTonCenterHistoricalLoader(cfg.TonCenterAPIKey, cfg.TonCenterRate)

		var startSeqno, endSeqno uint32
		if cfg.StartSeqNo > 0 {
			startSeqno = cfg.StartSeqNo
		} else {
			log.Fatal("Для режима toncenter необходимо указать START_SEQNO")
		}
		if cfg.EndSeqNo > 0 {
			endSeqno = cfg.EndSeqNo
		} else {
			log.Fatal("Для режима toncenter необходимо указать END_SEQNO")
		}

		log.Printf("TonCenter: загрузка блоков %d — %d (%d блоков, rate=%d req/sec)",
			startSeqno, endSeqno, endSeqno-startSeqno+1, cfg.TonCenterRate)

		go func() {
			if err := loader.LoadBlocks(ctx, startSeqno, endSeqno, metricsChan); err != nil {
				if ctx.Err() == nil {
					log.Printf("Ошибка загрузки TonCenter: %v", err)
				}
			}
			log.Println("TonCenter загрузка завершена")
			cancel()
		}()

	default:
		log.Fatalf("Неизвестный режим: %q (допустимые: realtime, historical, both, toncenter)", cfg.Mode)
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
