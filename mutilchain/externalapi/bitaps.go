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

const bitapsAPIURL = "https://api.bitaps.com/%s/v1/blockchain/%s"

var bitapsAddressDetailUrl = `address/state/%s`
var bitapsAddressTxsUrl = `address/transactions/%s`

type BitapsSummaryResponseData struct {
	Time float64           `json:"time"`
	Data BitapsSummaryData `json:"data"`
}

type BitapsSummaryData struct {
	Balance         int64 `json:"balance"`
	ReceivedAmount  int64 `json:"receivedAmount"`
	ReceivedTxCount int64 `json:"receivedTxCount"`
	SentAmount      int64 `json:"sentAmount"`
	SentTxCount     int64 `json:"sentTxCount"`
}

type BitapsAddressTxsResponseData struct {
	Data BitapsAddressTxsDataInner `json:"data"`
}

type BitapsAddressTxsDataInner struct {
	Page  int                 `json:"page"`
	Limit int                 `json:"limit"`
	Pages int                 `json:"pages"`
	List  []BitapsAddrtxsData `json:"list"`
}

type BitapsAddrtxsData struct {
	Txid          string `json:"txId"`
	BlockHeight   int64  `json:"blockHeight"`
	Timestamp     int64  `json:"timestamp"`
	Amount        int64  `json:"amount"`
	Fee           int64  `json:"fee"`
	Confirmations int64  `json:"confirmations"`
	Coinbase      bool   `json:"coinbase"`
}

func GetBitapsSummaryData(chainType, address string) (*BitapsSummaryResponseData, error) {
	var fetchData BitapsSummaryResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: fmt.Sprintf(bitapsAPIURL, chainType, fmt.Sprintf(bitapsAddressDetailUrl, address)),
		Payload: map[string]string{},
	}
	if err := HttpRequest(req, &fetchData); err != nil {
		return nil, err
	}
	return &fetchData, nil
}

func GetBitapsAddressTxsData(chainType, address string, limit, offset int64) (*BitapsAddressTxsResponseData, error) {
	var fetchData BitapsAddressTxsResponseData
	pageNumber := offset/limit + 1
	query := map[string]string{
		"page":  fmt.Sprintf("%d", pageNumber),
		"limit": fmt.Sprintf("%d", limit),
	}
	//insert api key
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: fmt.Sprintf(bitapsAPIURL, chainType, fmt.Sprintf(bitapsAddressTxsUrl, address)),
		Payload: query,
	}
	if err := HttpRequest(req, &fetchData); err != nil {
		return nil, err
	}
	return &fetchData, nil
}

func GetBitapsAddressInfoAPI(address, chainType string, limit, offset int64, txnType dbtypes.AddrTxnViewType) (*APIAddressInfo, error) {
	log.Debugf("Start get address data from Bitaps API for %s", chainType)
	//Get address summary data
	summaryData, err := GetBitapsSummaryData(chainType, address)
	if err != nil {
		return nil, err
	}
	//Get response code
	// txCount, _ := strconv.ParseInt(summaryData.Data[0].TxCount, 0, 32)
	// sendAmount, _ := strconv.ParseFloat(summaryData.Data[0].SendAmount, 64)
	// balance, _ := strconv.ParseFloat(summaryData.Data[0].Balance, 64)
	// receiveAmount, _ := strconv.ParseFloat(summaryData.Data[0].ReceiveAmount, 64)
	addressInfo := &APIAddressInfo{
		Address:         address,
		NumTransactions: summaryData.Data.ReceivedTxCount + summaryData.Data.SentTxCount,
		Sent:            summaryData.Data.SentAmount,
		Received:        summaryData.Data.ReceivedAmount,
		NumFundingTxns:  summaryData.Data.ReceivedTxCount,
		NumSpendingTxns: summaryData.Data.SentTxCount,
		NumUnconfirmed:  0,
		Unspent:         summaryData.Data.Balance,
	}
	//Get address transaction list
	addrTxsResponse, err := GetBitapsAddressTxsData(chainType, address, limit, offset)
	if err != nil {
		return nil, err
	}
	if len(addrTxsResponse.Data.List) == 0 {
		return nil, fmt.Errorf("Get address tx list data failed")
	}
	transactions := make([]*dbtypes.AddressTx, 0)
	for _, txData := range addrTxsResponse.Data.List {
		// coin, _ := strconv.ParseFloat(txData.Amount, 64)
		isFunding := txData.Amount > 0
		txTime, txSize, confirmations := GetMutilchainTxTimeSizeConfirmations(txData.Txid, chainType)
		addrTx := dbtypes.AddressTx{
			TxID:          txData.Txid,
			Size:          uint32(txSize),
			FormattedSize: humanize.Bytes(uint64(txSize)),
			Time:          dbtypes.NewTimeDef(time.Unix(txTime, 0)),
			Confirmations: uint64(confirmations),
			Coinbase:      txData.Coinbase,
		}
		amountValue := dcrutil.Amount(int64(math.Abs(float64(txData.Amount))))
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
	log.Debugf("Finish get address data from Bitaps API for %s", chainType)
	return addressInfo, nil
}
