package xmrclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/decred/dcrdata/v8/xmr/xmrutil"
)

// XMRClient is a simple JSON-RPC + direct-endpoint client for monerod.
type XMRClient struct {
	endpoint   string
	baseURL    string
	httpCli    *http.Client
	Username   string
	Password   string
	Timeout    time.Duration // per-request timeout use in postCore
	MaxBatch   int           // optional: batch size when prune mode
	MaxRetries int           // optional: retry num
}

// NewXMRClient preserves previous signature and uses 60s timeout.
func NewXMRClient(endpoint string) *XMRClient {
	return NewXMRClientWithTimeout(endpoint, 10*time.Minute)
}

func NewXMRClientWithTimeout(endpoint string, perRequestTimeout time.Duration) *XMRClient {
	ep := strings.TrimSpace(endpoint)

	base := ep
	if strings.HasSuffix(strings.ToLower(base), "/json_rpc") {
		base = base[:len(base)-len("/json_rpc")]
	}

	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          64,
		// Keep idle (kept-alive) connections to monerod low: each open socket
		// occupies an RPC slot on the node even when idle.
		MaxIdleConnsPerHost:   8,
		// Bound the TOTAL simultaneous connections to monerod. Without this
		// (Go's default is unlimited), the explorer's per-block/per-tx fan-out
		// of get_outs/get_transactions opens hundreds of sockets at once and
		// saturates monerod's RPC server, which refuses/resets new connections
		// (observed: node rejecting at ~26 conns while only 4% CPU — it is
		// connection/slot bound, not compute bound). 8 keeps headroom on the
		// node; excess calls queue transparently on the client pool.
		MaxConnsPerHost:       8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		DisableCompression:    false,
	}

	cli := &http.Client{
		Timeout:   10 * time.Minute,
		Transport: tr,
	}

	return &XMRClient{
		endpoint:   ep,
		httpCli:    cli,
		baseURL:    strings.TrimRight(base, "/"),
		Timeout:    perRequestTimeout,
		MaxBatch:   50,
		MaxRetries: 8,
	}
}

// internal rpc wrapper for JSON-RPC methods under /json_rpc
func (c *XMRClient) callRPC(method string, params interface{}, out interface{}) error {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "0",
		"method":  method,
		"params":  params,
	})
	resp, err := c.httpCli.Post(c.endpoint, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// decode wrapper
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		// try to read body for debug
		return fmt.Errorf("decode rpc response: %v", err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error: %s", rpcResp.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(rpcResp.Result, out)
}

// ---------- Existing helper methods (kept/compatible) ----------

func (c *XMRClient) GetBlockCount() (uint64, error) {
	var res struct {
		Count uint64 `json:"count"`
	}
	err := c.callRPC("get_block_count", nil, &res)
	return res.Count, err
}

func (c *XMRClient) GetConnections() (int, error) {
	var res struct {
		Incoming int `json:"incoming_connections_count"`
		Outgoing int `json:"outgoing_connections_count"`
	}
	err := c.callRPC("get_info", nil, &res)
	if err != nil {
		return 0, err
	}
	return res.Incoming + res.Outgoing, nil
}

func (c *XMRClient) GetBlockHeaderByHeight(height uint64) (*xmrutil.BlockHeader, error) {
	var res struct {
		BlockHeader xmrutil.BlockHeader `json:"block_header"`
	}
	err := c.callRPC("get_block_header_by_height", map[string]uint64{"height": height}, &res)
	return &res.BlockHeader, err
}

func (c *XMRClient) GetBlockHeaderByHash(hash string) (*xmrutil.BlockHeader, error) {
	var res struct {
		BlockHeader xmrutil.BlockHeader `json:"block_header"`
	}
	err := c.callRPC("get_block_header_by_hash", map[string]string{"hash": hash}, &res)
	if err != nil {
		return nil, err
	}
	return &res.BlockHeader, nil
}

func (c *XMRClient) GetBlock(height uint64) (*xmrutil.BlockResult, error) {
	var res xmrutil.BlockResult
	err := c.callRPC("get_block", map[string]uint64{"height": height}, &res)
	return &res, err
}

func (c *XMRClient) GetBlockByHash(hash string) (*xmrutil.BlockResult, error) {
	var res xmrutil.BlockResult
	err := c.callRPC("get_block", map[string]string{"hash": hash}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *XMRClient) GetLastBlockHeader() (*xmrutil.BlockHeader, error) {
	var res struct {
		BlockHeader xmrutil.BlockHeader `json:"block_header"`
	}
	err := c.callRPC("get_last_block_header", nil, &res)
	return &res.BlockHeader, err
}

func (c *XMRClient) GetInfo() (*xmrutil.BlockchainInfo, error) {
	var res xmrutil.BlockchainInfo
	err := c.callRPC("get_info", nil, &res)
	return &res, err
}

// ------------------ New additions ------------------

// GetTransactionsResult models the common fields returned by get_transactions.
type GetTransactionsResult struct {
	// Transaction payload
	TxsHashes   []string `json:"txs_hashes,omitempty"`
	TxsAsHex    []string `json:"txs_as_hex,omitempty"`
	TxsAsJSON   []string `json:"txs_as_json,omitempty"`
	MissedTx    []string `json:"missed_tx,omitempty"`
	MissedTxIDs []string `json:"missed_txids,omitempty"`
	InvalidTxs  []string `json:"invalid_txids,omitempty"`
	Status      string   `json:"status,omitempty"`
	TopHash     string   `json:"top_hash,omitempty"`
	Credits     uint64   `json:"credits,omitempty"`
	Txs         []TxInfo `json:"txs,omitempty"`
}

type TxInfo struct {
	AsHex           string   `json:"as_hex"`
	AsJSON          string   `json:"as_json"`
	BlockHeight     int64    `json:"block_height"`
	BlockTimestamp  uint64   `json:"block_timestamp"`
	Confirmations   uint64   `json:"confirmations"`
	DoubleSpendSeen bool     `json:"double_spend_seen"`
	InPool          bool     `json:"in_pool"`
	OutputIndices   []uint64 `json:"output_indices"`
	PrunableAsHex   string   `json:"prunable_as_hex"`
	PrunableHash    string   `json:"prunable_hash"`
	PrunedAsHex     string   `json:"pruned_as_hex"`
	TxHash          string   `json:"tx_hash"`
}

type truncatedJSONError struct {
	msg string
}

func (e *truncatedJSONError) Error() string { return e.msg }

type httpStatusError struct {
	Code       int
	BodySample string
}

func (e *httpStatusError) Error() string {
	if len(e.BodySample) > 0 {
		return fmt.Sprintf("daemon returned status %d: %s", e.Code, e.BodySample)
	}
	return fmt.Sprintf("daemon returned status %d", e.Code)
}

func isRecoverableStatus(code int) bool {
	if code == 408 || code == 413 || code == 429 {
		return true
	}
	if code >= 500 && code <= 599 {
		return true
	}
	return false
}

func backoff(attempt int) time.Duration {
	// 250ms, 500ms, 1s, 2s (cap ~2s)
	d := 250 * time.Millisecond
	for i := 0; i < attempt; i++ {
		d *= 2
		if d > 2*time.Second {
			return 2 * time.Second
		}
	}
	return d
}

func (c *XMRClient) postCore(endpoint string, params interface{}, out interface{}) error {
	client := c.httpCli
	if client == nil {
		tr := &http.Transport{
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			DisableCompression:    false,
		}
		client = &http.Client{
			Timeout:   0,
			Transport: tr,
		}
	}

	url := strings.TrimRight(c.baseURL, "/") + "/" + strings.TrimLeft(endpoint, "/")

	bodyBytes, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	reqTimeout := 120 * time.Second
	if c.Timeout > 0 {
		reqTimeout = c.Timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), reqTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		// timeouts, conn reset, etc.
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		limited := io.LimitReader(resp.Body, 1024)
		sample, _ := io.ReadAll(limited)
		return &httpStatusError{Code: resp.StatusCode, BodySample: string(sample)}
	}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) ||
			strings.Contains(err.Error(), "unexpected end of JSON") {
			return &truncatedJSONError{msg: "truncated JSON response"}
		}
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// GetTransactions calls get_transactions with given hashes. If decodeAsJSON true it will request txs_as_json.
func (c *XMRClient) GetTransactions(hashes []string, decodeAsJSON bool) (*GetTransactionsResult, error) {
	if len(hashes) == 0 {
		return &GetTransactionsResult{Status: "OK"}, nil
	}

	const (
		maxRetriesPerCall  = 3
		batchPrunedDefault = 50
		batchAsJSONDefault = 1
	)

	batchSize := batchPrunedDefault
	prune := !decodeAsJSON
	if decodeAsJSON {
		batchSize = batchAsJSONDefault
	}

	if c.MaxBatch > 0 {
		batchSize = c.MaxBatch
	}
	if decodeAsJSON && batchSize != 1 {
		batchSize = 1
	}

	agg := &GetTransactionsResult{Status: "OK"}

	for start := 0; start < len(hashes); start += batchSize {
		end := start + batchSize
		if end > len(hashes) {
			end = len(hashes)
		}
		batch := hashes[start:end]

		// Call with retry/backoff + fallback
		var lastErr error

		// attempt loop for batch
		for attempt := 0; attempt < maxRetriesPerCall; attempt++ {
			params := map[string]interface{}{
				"txs_hashes":     batch,
				"decode_as_json": decodeAsJSON,
			}
			if !decodeAsJSON {
				params["prune"] = prune
			}

			var tmp GetTransactionsResult
			err := c.postCore("get_transactions", params, &tmp)
			if err == nil {
				agg.Credits += tmp.Credits
				if tmp.TopHash != "" {
					agg.TopHash = tmp.TopHash
				}
				agg.Txs = append(agg.Txs, tmp.Txs...)
				agg.MissedTx = append(agg.MissedTx, tmp.MissedTx...)
				agg.MissedTx = append(agg.MissedTx, tmp.MissedTxIDs...)
				agg.TxsAsJSON = append(agg.TxsAsJSON, tmp.TxsAsJSON...)
				agg.TxsAsHex = append(agg.TxsAsHex, tmp.TxsAsHex...)
				agg.TxsHashes = append(agg.TxsHashes, tmp.TxsHashes...)
				lastErr = nil
				break
			}

			lastErr = err

			var tje *truncatedJSONError
			var hse *httpStatusError

			switch {
			case errors.As(err, &tje):
				if len(batch) > 1 {
					if e := c.fetchEachIndividually(batch, decodeAsJSON, prune, agg); e != nil {
						lastErr = e
					} else {
						lastErr = nil
					}
					attempt = maxRetriesPerCall
					break
				}
				if decodeAsJSON {
					decodeAsJSON = false
					prune = true
					continue
				}

			case errors.As(err, &hse):
				if isRecoverableStatus(hse.Code) {
					if len(batch) > 1 {
						if e := c.fetchEachIndividually(batch, decodeAsJSON, prune, agg); e != nil {
							lastErr = e
						} else {
							lastErr = nil
						}
						attempt = maxRetriesPerCall
						break
					}
					if decodeAsJSON {
						decodeAsJSON = false
						prune = true
						continue
					}
				}
			default:
			}

			// backoff trước khi thử lại (nếu còn lượt)
			if attempt < maxRetriesPerCall-1 {
				time.Sleep(backoff(attempt))
			}
		}

		if lastErr != nil {
			return nil, fmt.Errorf("get_transactions batch [%d:%d] failed: %w", start, end, lastErr)
		}
	}

	return agg, nil
}

func (c *XMRClient) fetchEachIndividually(batch []string, decodeAsJSON bool, prune bool, agg *GetTransactionsResult) error {
	for _, h := range batch {
		for step := 0; step < 2; step++ {
			params := map[string]interface{}{
				"txs_hashes":     []string{h},
				"decode_as_json": decodeAsJSON,
			}
			if !decodeAsJSON {
				params["prune"] = prune
			}

			var single GetTransactionsResult
			err := c.postCore("get_transactions", params, &single)
			if err == nil {
				agg.Credits += single.Credits
				if single.TopHash != "" {
					agg.TopHash = single.TopHash
				}
				agg.Txs = append(agg.Txs, single.Txs...)
				agg.MissedTx = append(agg.MissedTx, single.MissedTx...)
				agg.MissedTx = append(agg.MissedTx, single.MissedTxIDs...)
				agg.TxsAsJSON = append(agg.TxsAsJSON, single.TxsAsJSON...)
				agg.TxsAsHex = append(agg.TxsAsHex, single.TxsAsHex...)
				agg.TxsHashes = append(agg.TxsHashes, single.TxsHashes...)
				break
			}

			var tje *truncatedJSONError
			var hse *httpStatusError
			if step == 0 && decodeAsJSON &&
				(errors.As(err, &tje) || (errors.As(err, &hse) && isRecoverableStatus(hse.Code))) {
				decodeAsJSON = false
				prune = true
				continue
			}

			return fmt.Errorf("get_transactions failed for %s: %w", h, err)
		}
	}
	return nil
}

// ----------------- Direct (non-json_rpc) endpoints for mempool -----------------

// TxPoolEntry models a single entry in the transaction pool (fields commonly used).
type TxPoolEntry struct {
	IDHash      string `json:"id_hash,omitempty"`
	Blob        string `json:"tx_blob,omitempty"`
	BlobSize    int64  `json:"blob_size,omitempty"`
	Fee         uint64 `json:"fee,omitempty"` // fee atomic units
	TxJSON      string `json:"tx_json,omitempty"`
	ReceiveTime uint64 `json:"receive_time,omitempty"`
	Relayed     bool   `json:"relayed,omitempty"`
	KeptByBlock bool   `json:"kept_by_block,omitempty"`
	FailReason  string `json:"last_failed_reason,omitempty"`
}

type TxPoolResult struct {
	Transactions []TxPoolEntry `json:"transactions,omitempty"`
	PoolSize     int           `json:"pool_size,omitempty"`
	Status       string        `json:"status,omitempty"`
}

// helper to call direct daemon endpoints (POST to /get_transaction_pool etc)
func (c *XMRClient) postDirect(path string, body interface{}, out interface{}) error {
	url := c.baseURL + path
	var buf io.Reader
	if body != nil {
		bb, _ := json.Marshal(body)
		buf = bytes.NewReader(bb)
	} else {
		buf = bytes.NewReader([]byte("{}"))
	}
	resp, err := c.httpCli.Post(url, "application/json", buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		// discard
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GetTransactionPool returns full mempool entries (decode_as_json not required)
func (c *XMRClient) GetTransactionPool() (*TxPoolResult, error) {
	var res TxPoolResult
	if err := c.postDirect("/get_transaction_pool", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetTransactionPoolHashes returns only pool hashes (lightweight)
func (c *XMRClient) GetTransactionPoolHashes() ([]string, error) {
	var res struct {
		PoolHashes []string `json:"tx_hashes"` // some versions use tx_hashes
		TxHashes   []string `json:"tx_hashes"`
		Status     string   `json:"status"`
	}
	// endpoint path
	if err := c.postDirect("/get_transaction_pool_hashes", nil, &res); err != nil {
		return nil, err
	}
	// prefer TxHashes then PoolHashes
	if len(res.TxHashes) > 0 {
		return res.TxHashes, nil
	}
	return res.PoolHashes, nil
}

// GetTransactionPoolStats returns pool stats (count/bytes/fees)
func (c *XMRClient) GetTransactionPoolStats() (*xmrutil.GetTransactionPoolStatsResponse, error) {
	var res xmrutil.GetTransactionPoolStatsResponse
	if err := c.postDirect("/get_transaction_pool_stats", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *XMRClient) GetOuts(globalIndices []uint64) (*xmrutil.GetOutsResult, error) {
	// prepare params
	outputs := make([]map[string]interface{}, len(globalIndices))
	for i, index := range globalIndices {
		outputs[i] = map[string]interface{}{
			"index": index,
		}
	}
	params := map[string]interface{}{
		"get_txid": true,
		"outputs":  outputs,
	}

	// Call endpoint /get_outs
	var res xmrutil.GetOutsResult
	err := c.postCore("get_outs", params, &res)
	if err != nil {
		return nil, fmt.Errorf("failed to call get_outs: %w", err)
	}

	// Check status
	if res.Status != "OK" {
		return nil, fmt.Errorf("get_outs returned non-OK status: %s", res.Status)
	}

	return &res, nil
}
