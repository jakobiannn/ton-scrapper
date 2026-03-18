package collector

import (
	"context"
	"log"

	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/ton"
)

type TONClient struct {
	api *ton.APIClient
}

func NewTONClient(configURL string) (*TONClient, error) {
	client := liteclient.NewConnectionPool()

	err := client.AddConnectionsFromConfigUrl(context.Background(), configURL)
	if err != nil {
		return nil, err
	}

	api := ton.NewAPIClient(client)

	log.Println("Подключено к TON")

	return &TONClient{api: api}, nil
}

func (c *TONClient) GetAPI() *ton.APIClient {
	return c.api
}
