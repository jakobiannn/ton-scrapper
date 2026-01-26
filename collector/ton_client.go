package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type TonAPIClient struct {
	baseURL string
	client  *http.Client
	apiKey  string
}

type BlocksResponse struct {
	Blocks []BlockInfo `json:"blocks"`
}

type BlockInfo struct {
	Workchain  int    `json:"workchain"`
	Shard      string `json:"shard"`
	Seqno      int    `json:"seqno"`
	RootHash   string `json:"root_hash"`
	FileHash   string `json:"file_hash"`
	GenUtime   int64  `json:"gen_utime"`
	TxQuantity int    `json:"tx_quantity"`
}

type TransactionsResponse struct {
	Transactions []Transaction `json:"transactions"`
}

type Transaction struct {
	Account   AccountAddress `json:"account"`
	Hash      string         `json:"hash"`
	Lt        int64          `json:"lt"`
	Utime     int64          `json:"utime"`
	TotalFees int64          `json:"total_fees"`
	InMsg     *TxMessage     `json:"in_msg"`
	OutMsgs   []TxMessage    `json:"out_msgs"`
}

type AccountAddress struct {
	Address  string `json:"address"`
	Name     string `json:"name,omitempty"`
	IsScam   bool   `json:"is_scam"`
	IsWallet bool   `json:"is_wallet"`
}

type TxMessage struct {
	Source      *AccountAddress `json:"source"`
	Destination *AccountAddress `json:"destination"`
	Value       int64           `json:"value"`
	Hash        string          `json:"hash"`
}

func NewTonAPIClient(apiKey string) *TonAPIClient {
	return &TonAPIClient{
		baseURL: "https://tonapi.io/v2",
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		apiKey: apiKey,
	}
}

func (c *TonAPIClient) doRequest(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

func (c *TonAPIClient) GetLatestBlocks(ctx context.Context, limit int) (*BlocksResponse, error) {
	url := fmt.Sprintf("%s/blockchain/blocks?limit=%d", c.baseURL, limit)

	body, err := c.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var response BlocksResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func (c *TonAPIClient) GetBlockTransactions(ctx context.Context, workchain int, shard string, seqno int) (*TransactionsResponse, error) {
	// Формат block_id: (workchain,shard,seqno)
	blockID := fmt.Sprintf("(%d,%s,%d)", workchain, shard, seqno)
	url := fmt.Sprintf("%s/blockchain/blocks/%s/transactions", c.baseURL, blockID)

	body, err := c.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var response TransactionsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func (c *TonAPIClient) GetBlockInfo(ctx context.Context, workchain int, shard string, seqno int) (*BlockInfo, error) {
	blockID := fmt.Sprintf("(%d,%s,%d)", workchain, shard, seqno)
	url := fmt.Sprintf("%s/blockchain/blocks/%s", c.baseURL, blockID)

	body, err := c.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var block BlockInfo
	if err := json.Unmarshal(body, &block); err != nil {
		return nil, err
	}

	return &block, nil
}
