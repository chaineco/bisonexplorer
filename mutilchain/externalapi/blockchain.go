package externalapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/dustin/go-humanize"
)

var blockchainUrl = `https://blockchain.info/rawaddr/%s`

type BlockchainAddressData struct {
	Hash160       string                          `json:"hash160"`
	Address       string                          `json:"address"`
	Ntx           int64                           `json:"n_tx"`
	NUnredeemed   int64                           `json:"n_unredeemed"`
	TotalReceived int64                           `json:"total_received"`
	TotalSent     int64                           `json:"total_sent"`
	FinalBalance  int64                           `json:"final_balance"`
	Txs           []BlockchainAddressTransactions `json:"txs"`
}

type BlockchainAddressTransactions struct {
	Hash        string                              `json:"hash"`
	Ver         int32                               `json:"ver"`
	VinSz       int64                               `json:"vin_sz"`
	VoutSz      int64                               `json:"vout_sz"`
	Size        int64                               `json:"size"`
	Weight      int64                               `json:"weight"`
	Fee         int64                               `json:"fee"`
	RelayedBy   string                              `json:"relayed_by"`
	LockTime    int64                               `json:"lock_time"`
	TxIndex     int64                               `json:"tx_index"`
	DoubleSpend bool                                `json:"double_spend"`
	Time        int64                               `json:"time"`
	BlockIndex  int64                               `json:"block_index"`
	BlockHeight int64                               `json:"block_height"`
	Inputs      []BlockchainAddressTransactionInput `json:"inputs"`
	Out         []BlockchainAddressTransactionOut   `json:"out"`
	Result      int64                               `json:"result"`
	Balance     int64                               `json:"balance"`
	Rbf         bool                                `json:"rbf"`
}

type BlockchainAddressTransactionInput struct {
	Sequence int64                           `json:"sequence"`
	Witness  string                          `json:"witness"`
	Script   string                          `json:"script"`
	Index    int                             `json:"index"`
	PrevOut  BlockchainAddressTransactionOut `json:"prev_out"`
}

type BlockchainAddressTransactionOut struct {
	Type              int                                   `json:"type"`
	Spent             bool                                  `json:"spent"`
	Value             int64                                 `json:"value"`
	SpendingOutpoints []BlockchainAddressTxSpendingOutpoint `json:"spending_outpoints"`
	N                 int                                   `json:"n"`
	TxIndex           int64                                 `json:"tx_index"`
	Script            string                                `json:"script"`
	Address           string                                `json:"addr"`
}

type BlockchainAddressTxSpendingOutpoint struct {
	TxIndex int64 `json:"tx_index"`
	N       int   `json:"n"`
}

func GetBlockchainInfoAddressInfoAPI(address string, chainType string, limit, offset, chainHeight int64) (*APIAddressInfo, error) {
	//if chain is not support, return false
	if chainType == mutilchain.TYPELTC {
		return nil, fmt.Errorf("%s", "Blockchain.com does not support this blockchain")
	}
	log.Infof("Start get address data from Blockchain Info for %s", chainType)
	var loopNum int64
	if limit > 50 {
		loopNum = limit/50 + 1
	} else {
		loopNum = 1
	}
	var result BlockchainAddressData
	for i := 1; i <= int(loopNum); i++ {
		var tempAddrData BlockchainAddressData
		url := fmt.Sprintf(blockchainUrl, address)
		//fetch data
		query := map[string]string{
			"limit":  fmt.Sprintf("%d", 50),
			"offset": fmt.Sprintf("%d", offset+int64((i-1)*50)),
		}
		req := &ReqConfig{
			Method:  http.MethodGet,
			HttpUrl: url,
			Payload: query,
		}
		if err := HttpRequest(req, &tempAddrData); err != nil {
			return nil, err
		}
		isBreak := false
		if tempAddrData.Ntx <= int64(i*50) {
			isBreak = true
		}
		if i == 1 {
			result = tempAddrData
			if isBreak {
				break
			} else {
				continue
			}
		}
		result.Txs = append(result.Txs, tempAddrData.Txs...)
		if isBreak {
			break
		}
	}
	transactions := make([]*dbtypes.AddressTx, 0, len(result.Txs))
	numUnconfirmed := int64(0)
	//handler tx for address info
	for _, tx := range result.Txs {
		addrTx := dbtypes.AddressTx{
			TxID:          tx.Hash,
			Size:          uint32(tx.Size),
			FormattedSize: humanize.Bytes(uint64(tx.Size)),
			Time:          dbtypes.NewTimeDef(time.Unix(tx.Time, 0)),
		}
		//check if is funding
		matchedTxOut, isFunding := CheckIsFundingTransaction(tx.Out, address)
		var matchedTxIn *BlockchainAddressTransactionInput
		isSpending := false
		if !isFunding {
			//if is not funding tx, check if is spending
			matchedTxIn, isSpending = CheckIsSpendingTransaction(tx.Inputs, address)
			if !isSpending {
				//if is not spending, ignore this transaction, continue
				continue
			}
		}
		if tx.Time > 0 {
			addrTx.Confirmations = uint64(chainHeight - tx.BlockHeight + 1)
		} else {
			numUnconfirmed++
			addrTx.Confirmations = 0
		}
		if isFunding {
			coin := dbtypes.GetMutilchainCoinAmount(matchedTxOut.Value, chainType)
			isSpending := matchedTxOut.Spent
			addrTx.ReceivedTotal = coin
			if isSpending {
				//get txIndex
				if len(matchedTxOut.SpendingOutpoints) > 0 {
					spendingOutpoint := matchedTxOut.SpendingOutpoints[0]
					addrTx.MatchedTx = GetMatchedTxHash(result.Txs, spendingOutpoint.TxIndex)
				}
			}
			addrTx.Total = coin
		} else {
			coin := dbtypes.GetMutilchainCoinAmount(matchedTxIn.PrevOut.Value, chainType)
			addrTx.SentTotal = coin
			addrTx.Total = coin
		}
		addrTx.IsFunding = isFunding
		transactions = append(transactions, &addrTx)
	}
	addressInfo := &APIAddressInfo{
		Address:         address,
		Transactions:    transactions,
		Received:        result.TotalReceived,
		Sent:            result.TotalSent,
		Unspent:         result.TotalReceived - result.TotalSent,
		NumUnconfirmed:  numUnconfirmed,
		NumTransactions: result.Ntx,
		NumFundingTxns:  result.Ntx,
		NumSpendingTxns: result.Ntx,
	}
	log.Infof("Finish get address data from Blockchain Info for %s", chainType)
	return addressInfo, nil
}

func GetMatchedTxHash(Txs []BlockchainAddressTransactions, txIndex int64) string {
	for _, tx := range Txs {
		if tx.TxIndex == txIndex {
			return tx.Hash
		}
	}
	return ""
}

func CheckIsFundingTransaction(txOuts []BlockchainAddressTransactionOut, address string) (*BlockchainAddressTransactionOut, bool) {
	for _, txOut := range txOuts {
		if txOut.Address == address {
			return &txOut, true
		}
	}
	return nil, false
}

func CheckIsSpendingTransaction(txIns []BlockchainAddressTransactionInput, address string) (*BlockchainAddressTransactionInput, bool) {
	for _, txIn := range txIns {
		if txIn.PrevOut.Address == address {
			return &txIn, true
		}
	}
	return nil, false
}
