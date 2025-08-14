package externalapi

import (
	"fmt"

	btcClient "github.com/btcsuite/btcd/rpcclient"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/mutilchain/btcrpcutils"
	"github.com/decred/dcrdata/v8/mutilchain/ltcrpcutils"
	ltcClient "github.com/ltcsuite/ltcd/rpcclient"
)

var (
	LTCClient *ltcClient.Client
	BTCClient *btcClient.Client
)

type APIAddressInfo struct {
	Address         string
	Transactions    []*dbtypes.AddressTx
	NumTransactions int64
	NumFundingTxns  int64
	NumSpendingTxns int64
	Received        int64
	Sent            int64
	Unspent         int64
	NumUnconfirmed  int64
}

const (
	BLockchainAPI  = "blockchain"
	BlockcypherAPI = "blockcypher"
	BitapsAPI      = "bitaps"
)

var APIList = []string{BLockchainAPI, BitapsAPI}

func GetAPIMutilchainAddressDetails(okLinkAPIKey, address string, chainType string, limit, offset, chainHeight int64, txnType dbtypes.AddrTxnViewType) (*APIAddressInfo, error) {
	for _, api := range APIList {
		if chainType == mutilchain.TYPELTC && api == BLockchainAPI {
			continue
		}
		//Get from API
		addrInfo, err := GetAddressDetailsByAPIEnv(okLinkAPIKey, address, chainType, api, limit, offset, chainHeight, txnType)
		if err == nil {
			return addrInfo, nil
		}
	}
	return nil, fmt.Errorf("%s", "Get address info from all API failed")
}

func GetAddressDetailsByAPIEnv(okLinkAPIKey, address, chainType, apiType string, limit, offset, chainHeight int64, txnType dbtypes.AddrTxnViewType) (*APIAddressInfo, error) {
	switch apiType {
	case BLockchainAPI:
		return GetBlockchainInfoAddressInfoAPI(address, chainType, limit, offset, chainHeight)
	case BitapsAPI:
		return GetBitapsAddressInfoAPI(address, chainType, limit, offset, txnType)
	default:
		return nil, fmt.Errorf("%s%s", "Get by API failed, API type:", apiType)
	}
}

func GetMutilchainTxTimeSizeConfirmations(txHash, chainType string) (int64, int64, int64) {
	switch chainType {
	case mutilchain.TYPELTC:
		if LTCClient == nil {
			return 0, 0, 0
		}
		txRawRes, err := ltcrpcutils.GetRawTransactionByTxidStr(LTCClient, txHash)
		if err != nil {
			return 0, 0, 0
		}
		return txRawRes.Time, int64(txRawRes.Size), int64(txRawRes.Confirmations)
	case mutilchain.TYPEBTC:
		if BTCClient == nil {
			return 0, 0, 0
		}
		txRawRes, err := btcrpcutils.GetRawTransactionByTxidStr(BTCClient, txHash)
		if err != nil {
			return 0, 0, 0
		}
		return txRawRes.Time, int64(txRawRes.Size), int64(txRawRes.Confirmations)
	}
	return 0, 0, 0
}
