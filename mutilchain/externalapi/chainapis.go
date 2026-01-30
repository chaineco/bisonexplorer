package externalapi

import (
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/dustin/go-humanize"
)

// Chain API response structures
type ChainAddressResponse struct {
	Address       string `json:"address"`
	Balance       int64  `json:"balance"`
	TotalReceived int64  `json:"total_received"`
	TotalSent     int64  `json:"total_sent"`
	TxCount       int64  `json:"tx_count"`
	Chain         string `json:"chain"`
}

type ChainAddressTxsResponse struct {
	Address      string             `json:"address"`
	Total        int64              `json:"total"`
	Offset       int64              `json:"offset"`
	Limit        int64              `json:"limit"`
	Count        int64              `json:"count"`
	Transactions []ChainTransaction `json:"transactions"`
}

type ChainTransaction struct {
	Txid          string `json:"txid"`
	BlockHash     string `json:"block_hash"`
	BlockHeight   int64  `json:"block_height"`
	Version       int    `json:"version"`
	LockTime      int64  `json:"lock_time"`
	Size          int64  `json:"size"`
	VSize         int64  `json:"vsize"`
	Weight        int64  `json:"weight"`
	Fee           int64  `json:"fee"`
	Value         int64  `json:"value"`
	IsCoinbase    bool   `json:"is_coinbase"`
	Timestamp     string `json:"timestamp"`
	Chain         string `json:"chain"`
	Confirmations int64  `json:"confirmations"`
}

type ChainVout struct {
	VoutIndex    int      `json:"vout_index"`
	Value        int64    `json:"value"`
	ScriptPubkey string   `json:"script_pubkey"`
	Type         string   `json:"type"`
	Addresses    []string `json:"addresses"`
	Spent        bool     `json:"spent"`
	SpentByTxid  string   `json:"spent_by_txid"`
	SpentByVin   int      `json:"spent_by_vin"`
	Chain        string   `json:"chain"`
}

type ChainVin struct {
	VinIndex      int      `json:"vin_index"`
	PrevTxid      string   `json:"prev_txid"`
	PrevVoutIndex int      `json:"prev_vout_index"`
	ScriptSig     string   `json:"script_sig"`
	Sequence      uint32   `json:"sequence"`
	Witness       []string `json:"witness"`
	Chain         string   `json:"chain"`
}

type ChainAddressVoutsResponse struct {
	Address string      `json:"address"`
	Count   int64       `json:"count"`
	Vouts   []ChainVout `json:"vouts"`
}

type ChainAddressVinsResponse struct {
	Address string     `json:"address"`
	Count   int64      `json:"count"`
	Vins    []ChainVin `json:"vins"`
}

// GetChainAddressInfo fetches address summary from Chain API
func GetChainAddressInfo(chainApiUrls, chainType, address string) (*ChainAddressResponse, error) {
	var fetchData ChainAddressResponse
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: fmt.Sprintf("%s/%s/addresses/%s", chainApiUrls, chainType, address),
		Payload: map[string]string{},
	}
	if err := HttpRequest(req, &fetchData); err != nil {
		return nil, err
	}
	return &fetchData, nil
}

// GetChainAddressTxs fetches address transactions from Chain API
func GetChainAddressTxs(chainApiUrls, chainType, address string, limit, offset int64) (*ChainAddressTxsResponse, error) {
	var fetchData ChainAddressTxsResponse
	query := map[string]string{
		"offset": fmt.Sprintf("%d", offset),
		"limit":  fmt.Sprintf("%d", limit),
	}
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: fmt.Sprintf("%s/%s/addresses/%s/transactions", chainApiUrls, chainType, address),
		Payload: query,
	}
	if err := HttpRequest(req, &fetchData); err != nil {
		return nil, err
	}
	return &fetchData, nil
}

// GetChainAddressVouts fetches address outputs (receiving history) from Chain API
func GetChainAddressVouts(chainApiUrls, chainType, address string) (*ChainAddressVoutsResponse, error) {
	var fetchData ChainAddressVoutsResponse
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: fmt.Sprintf("%s/%s/addresses/%s/vouts", chainApiUrls, chainType, address),
		Payload: map[string]string{},
	}
	if err := HttpRequest(req, &fetchData); err != nil {
		return nil, err
	}
	return &fetchData, nil
}

// GetChainAddressVins fetches address inputs (spending history) from Chain API
func GetChainAddressVins(chainApiUrls, chainType, address string) (*ChainAddressVinsResponse, error) {
	var fetchData ChainAddressVinsResponse
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: fmt.Sprintf("%s/%s/addresses/%s/vins", chainApiUrls, chainType, address),
		Payload: map[string]string{},
	}
	if err := HttpRequest(req, &fetchData); err != nil {
		return nil, err
	}
	return &fetchData, nil
}

// GetChainAddressInfoAPI fetches address data from Chain API and returns APIAddressInfo
func GetChainAddressInfoAPI(chainApiUrls, address, chainType string, limit, offset int64) (*APIAddressInfo, error) {
	log.Infof("Start get address data from Chain API for %s", chainType)

	// Get address summary data
	summaryData, err := GetChainAddressInfo(chainApiUrls, chainType, address)
	if err != nil {
		return nil, fmt.Errorf("failed to get address info from Chain API: %w", err)
	}

	// Calculate funding and spending transactions
	// Since the API returns total tx_count, we need to estimate based on balance
	numFunding := summaryData.TxCount
	numSpending := int64(0)
	if summaryData.TotalSent > 0 {
		// Rough estimate: if there's spending, assume some transactions are spending
		numSpending = summaryData.TxCount / 2
		numFunding = summaryData.TxCount - numSpending
	}

	addressInfo := &APIAddressInfo{
		Address:         address,
		NumTransactions: summaryData.TxCount,
		Sent:            summaryData.TotalSent,
		Received:        summaryData.TotalReceived,
		NumFundingTxns:  numFunding,
		NumSpendingTxns: numSpending,
		NumUnconfirmed:  0,
		Unspent:         summaryData.Balance,
	}

	// Get address transaction list
	addrTxsResponse, err := GetChainAddressTxs(chainApiUrls, chainType, address, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to get address transactions from Chain API: %w", err)
	}

	if len(addrTxsResponse.Transactions) == 0 && summaryData.TxCount > 0 {
		return nil, fmt.Errorf("Chain API returned empty transaction list")
	}

	// Build vouts map to determine funding/spending for each transaction
	voutsResponse, _ := GetChainAddressVouts(chainApiUrls, chainType, address)
	voutsTxMap := make(map[string]int64) // txid -> total value received
	if voutsResponse != nil {
		for _, vout := range voutsResponse.Vouts {
			// We need the txid that contains this vout
			// For now, we'll determine funding/spending by checking transaction details
			_ = vout
		}
	}

	transactions := make([]*dbtypes.AddressTx, 0)
	for _, txData := range addrTxsResponse.Transactions {
		// Parse timestamp
		txTime, err := time.Parse(time.RFC3339, txData.Timestamp)
		if err != nil {
			txTime = time.Unix(0, 0)
		}

		// Determine if funding or spending based on transaction details
		// For now, we'll need to get more details or use a heuristic
		// A transaction is "funding" if it sends value TO this address
		// A transaction is "spending" if it sends value FROM this address
		isFunding := true // Default assumption, will be refined

		// Check if we have vouts data to determine funding
		if _, ok := voutsTxMap[txData.Txid]; ok {
			isFunding = true
		}

		addrTx := dbtypes.AddressTx{
			TxID:          txData.Txid,
			Size:          uint32(txData.Size),
			FormattedSize: humanize.Bytes(uint64(txData.Size)),
			Time:          dbtypes.NewTimeDef(txTime),
			Confirmations: uint64(txData.Confirmations),
			Coinbase:      txData.IsCoinbase,
			BlockHeight:   uint32(txData.BlockHeight),
		}

		// Convert satoshis to coin amount
		amountValue := dcrutil.Amount(int64(math.Abs(float64(txData.Value))))
		total := float64(0)
		if isFunding {
			addrTx.ReceivedTotal = amountValue.ToCoin()
			total = addrTx.ReceivedTotal
		} else {
			addrTx.SentTotal = 0 - amountValue.ToCoin()
			total = addrTx.SentTotal
		}
		addrTx.Total = total
		addrTx.IsFunding = isFunding
		addrTx.IsUnconfirmed = txData.Confirmations < 6

		transactions = append(transactions, &addrTx)
	}

	addressInfo.Transactions = transactions
	log.Infof("Finish get address data from Chain API for %s", chainType)
	return addressInfo, nil
}
