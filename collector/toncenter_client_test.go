package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- helpers ---

// mockTonCenterServer создаёт тестовый HTTP сервер имитирующий TonCenter API.
func mockTonCenterServer(t *testing.T, handlers map[string]interface{}) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, responseObj := range handlers {
		body, _ := json.Marshal(responseObj)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
		})
	}
	return httptest.NewServer(mux)
}

func tonCenterOKResponse(result interface{}) map[string]interface{} {
	return map[string]interface{}{
		"ok":     true,
		"result": result,
	}
}

func tonCenterErrorResponse(errMsg string) map[string]interface{} {
	return map[string]interface{}{
		"ok":    false,
		"error": errMsg,
	}
}

func newTestTonCenterClient(serverURL string) *TonCenterClient {
	c := NewTonCenterClient("")
	c.baseURL = serverURL
	return c
}

// --- GetMasterchainInfo ---

func TestTonCenterClient_GetMasterchainInfo_Success(t *testing.T) {
	result := map[string]interface{}{
		"last": map[string]interface{}{
			"workchain": -1,
			"shard":     "-9223372036854775808",
			"seqno":     42000000,
			"root_hash": "ABCDEF",
			"file_hash": "123456",
		},
	}

	srv := mockTonCenterServer(t, map[string]interface{}{
		"/getMasterchainInfo": tonCenterOKResponse(result),
	})
	defer srv.Close()

	client := newTestTonCenterClient(srv.URL)
	info, err := client.GetMasterchainInfo(context.Background())
	if err != nil {
		t.Fatalf("GetMasterchainInfo: %v", err)
	}
	if info.Last.Seqno != 42000000 {
		t.Errorf("Seqno: got %d, want 42000000", info.Last.Seqno)
	}
	if info.Last.Workchain != -1 {
		t.Errorf("Workchain: got %d, want -1", info.Last.Workchain)
	}
}

func TestTonCenterClient_GetMasterchainInfo_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tonCenterErrorResponse("rate limit exceeded")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newTestTonCenterClient(srv.URL)
	_, err := client.GetMasterchainInfo(context.Background())
	if err == nil {
		t.Fatal("ожидали ошибку при API error response")
	}
}

func TestTonCenterClient_GetMasterchainInfo_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	client := newTestTonCenterClient(srv.URL)
	_, err := client.GetMasterchainInfo(context.Background())
	if err == nil {
		t.Fatal("ожидали ошибку при HTTP 500")
	}
}

func TestTonCenterClient_GetMasterchainInfo_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not valid json {{{"))
	}))
	defer srv.Close()

	client := newTestTonCenterClient(srv.URL)
	_, err := client.GetMasterchainInfo(context.Background())
	if err == nil {
		t.Fatal("ожидали ошибку при невалидном JSON")
	}
}

// --- GetBlockTransactions ---

func TestTonCenterClient_GetBlockTransactions_Success(t *testing.T) {
	result := map[string]interface{}{
		"id": map[string]interface{}{
			"workchain": -1,
			"shard":     "-9223372036854775808",
			"seqno":     100,
		},
		"req_count":  3,
		"incomplete": false,
		"transactions": []map[string]interface{}{
			{"account": "addr1", "lt": "1000", "hash": "hash1"},
			{"account": "addr2", "lt": "1001", "hash": "hash2"},
			{"account": "addr3", "lt": "1002", "hash": "hash3"},
		},
	}

	srv := mockTonCenterServer(t, map[string]interface{}{
		"/getBlockTransactions": tonCenterOKResponse(result),
	})
	defer srv.Close()

	client := newTestTonCenterClient(srv.URL)
	txs, err := client.GetBlockTransactions(context.Background(), -1, "-9223372036854775808", 100)
	if err != nil {
		t.Fatalf("GetBlockTransactions: %v", err)
	}
	if len(txs.Transactions) != 3 {
		t.Errorf("Transactions count: got %d, want 3", len(txs.Transactions))
	}
	if txs.Transactions[0].Account != "addr1" {
		t.Errorf("первая транзакция: account=%q, want addr1", txs.Transactions[0].Account)
	}
}

func TestTonCenterClient_GetBlockTransactions_Empty(t *testing.T) {
	result := map[string]interface{}{
		"req_count":    0,
		"incomplete":   false,
		"transactions": []interface{}{},
	}

	srv := mockTonCenterServer(t, map[string]interface{}{
		"/getBlockTransactions": tonCenterOKResponse(result),
	})
	defer srv.Close()

	client := newTestTonCenterClient(srv.URL)
	txs, err := client.GetBlockTransactions(context.Background(), -1, "-9223372036854775808", 200)
	if err != nil {
		t.Fatalf("GetBlockTransactions (empty): %v", err)
	}
	if len(txs.Transactions) != 0 {
		t.Errorf("ожидали 0 транзакций, получили %d", len(txs.Transactions))
	}
}

// --- GetTransactions ---

func TestTonCenterClient_GetTransactions_Success(t *testing.T) {
	result := []map[string]interface{}{
		{
			"account":    "EQD...",
			"hash":       "txhash1",
			"lt":         "500",
			"now":        1700000000,
			"total_fees": "1000000",
			"in_msg": map[string]interface{}{
				"source":      "EQS...",
				"destination": "EQD...",
				"value":       "2000000000",
				"fwd_fee":     "100000",
				"ihr_fee":     "0",
				"created_lt":  "499",
			},
			"out_msgs": []interface{}{},
		},
	}

	srv := mockTonCenterServer(t, map[string]interface{}{
		"/getTransactions": tonCenterOKResponse(result),
	})
	defer srv.Close()

	client := newTestTonCenterClient(srv.URL)
	txs, err := client.GetTransactions(context.Background(), "EQD...", "500", "txhash1", 1)
	if err != nil {
		t.Fatalf("GetTransactions: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("ожидали 1 транзакцию, получили %d", len(txs))
	}
	if txs[0].Account != "EQD..." {
		t.Errorf("Account: got %q", txs[0].Account)
	}
	if txs[0].TotalFees != "1000000" {
		t.Errorf("TotalFees: got %q", txs[0].TotalFees)
	}
	if txs[0].InMsg == nil {
		t.Error("InMsg не должен быть nil")
	}
}

// --- API key header ---

func TestTonCenterClient_SendsAPIKey(t *testing.T) {
	var receivedKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("X-API-Key")
		resp := tonCenterOKResponse(map[string]interface{}{
			"last": map[string]interface{}{"workchain": -1, "shard": "-9223372036854775808", "seqno": 1},
		})
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewTonCenterClient("my-secret-key")
	client.baseURL = srv.URL

	client.GetMasterchainInfo(context.Background())

	if receivedKey != "my-secret-key" {
		t.Errorf("X-API-Key header: got %q, want %q", receivedKey, "my-secret-key")
	}
}

func TestTonCenterClient_NoAPIKey_NoHeader(t *testing.T) {
	var receivedKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("X-API-Key")
		resp := tonCenterOKResponse(map[string]interface{}{
			"last": map[string]interface{}{"workchain": -1, "shard": "", "seqno": 1},
		})
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewTonCenterClient("") // нет ключа
	client.baseURL = srv.URL
	client.GetMasterchainInfo(context.Background())

	if receivedKey != "" {
		t.Errorf("без API ключа заголовок не должен отправляться, получили: %q", receivedKey)
	}
}

// --- context cancellation ---

func TestTonCenterClient_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Имитируем медленный сервер
		select {
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	client := newTestTonCenterClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // сразу отменяем

	_, err := client.GetMasterchainInfo(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку при отменённом контексте")
	}
}

// --- query parameters ---

func TestTonCenterClient_GetBlockTransactions_SendsCorrectParams(t *testing.T) {
	var receivedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		result := map[string]interface{}{
			"req_count":    0,
			"incomplete":   false,
			"transactions": []interface{}{},
		}
		json.NewEncoder(w).Encode(tonCenterOKResponse(result))
	}))
	defer srv.Close()

	client := newTestTonCenterClient(srv.URL)
	client.GetBlockTransactions(context.Background(), -1, "-9223372036854775808", 42000)

	if receivedQuery == "" {
		t.Fatal("query параметры не были отправлены")
	}

	// Проверяем что seqno есть в параметрах
	expectedSeqno := "seqno=42000"
	if !contains(receivedQuery, expectedSeqno) {
		t.Errorf("ожидали %q в query %q", expectedSeqno, receivedQuery)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(s) > 0 && fmt.Sprintf("%s", s) != "" && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
