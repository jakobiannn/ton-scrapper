package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type TonCenterClient struct {
	baseURL string
	client  *http.Client
	apiKey  string
}

type TonCenterResponse struct {
	Ok     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
}

type MasterchainInfo struct {
	Last struct {
		Workchain int    `json:"workchain"`
		Shard     string `json:"shard"` // Изменено на string
		Seqno     int    `json:"seqno"`
		RootHash  string `json:"root_hash"`
		FileHash  string `json:"file_hash"`
	} `json:"last"`
}

type BlockTransactions struct {
	ID           []interface{} `json:"id"`
	ReqCount     int           `json:"req_count"`
	Incomplete   bool          `json:"incomplete"`
	Transactions []ShortTxInfo `json:"transactions"`
}

type ShortTxInfo struct {
	Account string `json:"account"`
	Lt      string `json:"lt"`
	Hash    string `json:"hash"`
}

type TransactionDetail struct {
	Account   string          `json:"account"`
	Hash      string          `json:"hash"`
	Lt        string          `json:"lt"`
	Now       int64           `json:"now"`
	TotalFees string          `json:"total_fees"`
	InMsg     *MessageDetail  `json:"in_msg"`
	OutMsgs   []MessageDetail `json:"out_msgs"`
}

type MessageDetail struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Value       string `json:"value"`
	FwdFee      string `json:"fwd_fee"`
	IhrFee      string `json:"ihr_fee"`
	CreatedLt   string `json:"created_lt"`
}

func NewTonCenterClient(apiKey string) *TonCenterClient {
	return &TonCenterClient{
		baseURL: "https://toncenter.com/api/v2",
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		apiKey: apiKey,
	}
}

func (c *TonCenterClient) doRequest(ctx context.Context, method string, params map[string]string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/"+method, nil)
	if err != nil {
		return nil, err
	}

	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	q := req.URL.Query()
	for k, v := range params {
		q.Add(k, v)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var response TonCenterResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	if !response.Ok {
		return nil, fmt.Errorf("API error: %s", response.Error)
	}

	return response.Result, nil
}

func (c *TonCenterClient) GetMasterchainInfo(ctx context.Context) (*MasterchainInfo, error) {
	result, err := c.doRequest(ctx, "getMasterchainInfo", nil)
	if err != nil {
		return nil, err
	}

	var info MasterchainInfo
	if err := json.Unmarshal(result, &info); err != nil {
		return nil, err
	}

	return &info, nil
}

func (c *TonCenterClient) GetBlockTransactions(ctx context.Context, workchain int, shard string, seqno int) (*BlockTransactions, error) {
	params := map[string]string{
		"workchain": fmt.Sprintf("%d", workchain),
		"shard":     shard,
		"seqno":     fmt.Sprintf("%d", seqno),
	}

	result, err := c.doRequest(ctx, "getBlockTransactions", params)
	if err != nil {
		return nil, err
	}

	var txs BlockTransactions
	if err := json.Unmarshal(result, &txs); err != nil {
		return nil, err
	}

	return &txs, nil
}

func (c *TonCenterClient) GetTransactions(ctx context.Context, address string, lt string, hash string, limit int) ([]TransactionDetail, error) {
	params := map[string]string{
		"address": address,
		"limit":   fmt.Sprintf("%d", limit),
	}

	if lt != "" {
		params["lt"] = lt
	}
	if hash != "" {
		params["hash"] = hash
	}

	result, err := c.doRequest(ctx, "getTransactions", params)
	if err != nil {
		return nil, err
	}

	var txs []TransactionDetail
	if err := json.Unmarshal(result, &txs); err != nil {
		return nil, err
	}

	return txs, nil
}
