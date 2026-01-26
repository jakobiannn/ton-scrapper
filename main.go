package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"ton-scrapper/collector"
	"ton-scrapper/models"
)

func main() {
	log.Println("Запуск TON Collector (TonCenter)")

	apiKey := os.Getenv("TONCENTER_API_KEY")

	processor := collector.NewTonCenterProcessor(apiKey)

	metricsChan := make(chan *models.BlockMetrics, 100)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		for metrics := range metricsChan {
			log.Printf("Метрики собраны успешно %v\n", metrics)
		}
	}()

	go func() {
		client := collector.NewTonCenterClient(apiKey)
		info, err := client.GetMasterchainInfo(context.Background())
		if err != nil {
			log.Fatal("Не удалось получить текущий блок:", err)
		}

		startSeqno := uint32(info.Last.Seqno - 2)

		if err := processor.StreamBlocks(ctx, startSeqno, metricsChan); err != nil {
			log.Printf("Ошибка стриминга: %v", err)
		}
	}()

	<-sigChan
	log.Println("Остановка сервиса...")
	cancel()
	close(metricsChan)
}
