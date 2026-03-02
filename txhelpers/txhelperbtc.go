package txhelpers

import (
	"encoding/hex"
	"fmt"
	"strings"

	btcjson "github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/btcutil"
	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	btctxscript "github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	btcwire "github.com/btcsuite/btcd/wire"
)

var (
	btcZeroHash = chainhash.Hash{}
)

type BTCAddressOutpoints struct {
	Address   string
	Outpoints []*btcwire.OutPoint
	PrevOuts  []BTCPrevOut
	TxnsStore map[chainhash.Hash]*BTCTxWithBlockData
}

func NewBTCAddressOutpoints(address string) *BTCAddressOutpoints {
	return &BTCAddressOutpoints{
		Address:   address,
		TxnsStore: make(map[chainhash.Hash]*BTCTxWithBlockData),
	}
}

type BTCPrevOut struct {
	TxSpending       chainhash.Hash
	InputIndex       int
	PreviousOutpoint *btcwire.OutPoint
}

type BTCRawTransactionGetter interface {
	GetRawTransaction(txHash *chainhash.Hash) (*btcutil.Tx, error)
}

type BTCVerboseTransactionGetter interface {
	GetRawTransactionVerbose(txHash *chainhash.Hash) (*btcjson.TxRawResult, error)
	GetBlockVerboseTx(blockHash *chainhash.Hash) (*btcjson.GetBlockVerboseTxResult, error)
}

func BTCMsgTxFromHex(txhex string, version int32) (*btcwire.MsgTx, error) {
	msgTx := btcwire.NewMsgTx(version)
	if err := msgTx.Deserialize(hex.NewDecoder(strings.NewReader(txhex))); err != nil {
		return nil, err
	}
	return msgTx, nil
}

func TotalBTCVout(vouts []btcjson.Vout) btcutil.Amount {
	var total btcutil.Amount
	for _, v := range vouts {
		a, err := btcutil.NewAmount(v.Value)
		if err != nil {
			continue
		}
		total += a
	}
	return total
}

// IsZeroHash checks if the Hash is the zero hash.
func IsBTCZeroHash(hash chainhash.Hash) bool {
	return hash == btcZeroHash
}

func MsgBTCTxFromHex(txhex string, version int32) (*btcwire.MsgTx, error) {
	msgTx := btcwire.NewMsgTx(version)
	if err := msgTx.Deserialize(hex.NewDecoder(strings.NewReader(txhex))); err != nil {
		return nil, err
	}
	return msgTx, nil
}

func BTCOutPointAddresses(outPoint *btcwire.OutPoint, c BTCRawTransactionGetter,
	params *btcchaincfg.Params) ([]string, btcutil.Amount, error) {
	// The addresses are encoded in the pkScript, so we need to get the
	// raw transaction, and the TxOut that contains the pkScript.
	prevTx, err := WithTimeout(func() (*btcutil.Tx, error) {
		return c.GetRawTransaction(&outPoint.Hash)
	})
	if err != nil {
		return nil, 0, fmt.Errorf("unable to get raw transaction for %s", outPoint.Hash.String())
	}

	txOuts := prevTx.MsgTx().TxOut
	if len(txOuts) <= int(outPoint.Index) {
		return nil, 0, fmt.Errorf("PrevOut index (%d) is beyond the TxOuts slice (length %d)",
			outPoint.Index, len(txOuts))
	}

	// For the TxOut of interest, extract the list of addresses
	txOut := txOuts[outPoint.Index]
	_, txAddresses, _, err := btctxscript.ExtractPkScriptAddrs(txOut.PkScript, params)
	if err != nil {
		return nil, 0, fmt.Errorf("Invalid tx hash get address")
	}
	value := btcutil.Amount(txOut.Value)
	addresses := make([]string, 0, len(txAddresses))
	for _, txAddr := range txAddresses {
		addr := txAddr.String()
		addresses = append(addresses, addr)
	}
	return addresses, value, nil
}

type BTCTxnsStore map[chainhash.Hash]*BTCTxWithBlockData

type BTCTxWithBlockData struct {
	Tx          *btcwire.MsgTx
	BlockHeight int64
	BlockHash   string
	MemPoolTime int64
}

// MempoolAddressStore organizes AddressOutpoints by address.
type BTCMempoolAddressStore map[string]*BTCAddressOutpoints

func BTCTxOutpointsByAddr(txAddrOuts BTCMempoolAddressStore, msgTx *btcwire.MsgTx, params *btcchaincfg.Params) (newOuts int, addrs map[string]bool) {
	if txAddrOuts == nil {
		panic("TxAddressOutpoints: input map must be initialized: map[string]*AddressOutpoints")
	}

	// Check the addresses associated with the PkScript of each TxOut.
	hash := msgTx.TxHash()
	addrs = make(map[string]bool)
	for outIndex, txOut := range msgTx.TxOut {
		_, txOutAddrs, _, _ := txscript.ExtractPkScriptAddrs(txOut.PkScript, params)
		if len(txOutAddrs) == 0 {
			continue
		}
		newOuts++
		txHash, err := chainhash.NewHashFromStr(hash.String())
		if err != nil {
			continue
		}
		// Check if we are watching any address for this TxOut.
		for _, txAddr := range txOutAddrs {
			addr := txAddr.String()

			op := btcwire.NewOutPoint(txHash, uint32(outIndex))

			addrOuts := txAddrOuts[addr]
			if addrOuts == nil {
				addrOuts = &BTCAddressOutpoints{
					Address:   addr,
					Outpoints: []*btcwire.OutPoint{op},
				}
				txAddrOuts[addr] = addrOuts
				addrs[addr] = true // new
				continue
			}
			if _, found := addrs[addr]; !found {
				addrs[addr] = false // not new to the address store
			}
			addrOuts.Outpoints = append(addrOuts.Outpoints, op)
		}
	}
	return
}

// TotalOutFromMsgTx computes the total value out of a MsgTx
func BTCTotalOutFromMsgTx(msgTx *btcwire.MsgTx) btcutil.Amount {
	var amtOut int64
	for _, v := range msgTx.TxOut {
		amtOut += v.Value
	}
	return btcutil.Amount(amtOut)
}

func BTCTxPrevOutsByAddr(txAddrOuts BTCMempoolAddressStore, txnsStore BTCTxnsStore, msgTx *btcwire.MsgTx, c BTCVerboseTransactionGetter,
	params *btcchaincfg.Params) (newPrevOuts int, addrs map[string]bool, valsIn []int64) {
	if txAddrOuts == nil {
		panic("BTCTxPrevOutsByAddr: input map must be initialized: map[string]*AddressOutpoints")
	}
	if txnsStore == nil {
		panic("BTCTxPrevOutsByAddr: input map must be initialized: map[string]*AddressOutpoints")
	}

	// Send all the raw transaction requests
	type promiseGetRawTransaction struct {
		result *btcjson.TxRawResult
		inIdx  int
	}
	promisesGetRawTransaction := make([]promiseGetRawTransaction, 0, len(msgTx.TxIn))

	for inIdx, txIn := range msgTx.TxIn {
		hash := &txIn.PreviousOutPoint.Hash
		if btcZeroHash.IsEqual(hash) {
			continue // coinbase or stakebase
		}
		txVerbose, txErr := WithTimeout(func() (*btcjson.TxRawResult, error) {
			return c.GetRawTransactionVerbose(hash)
		})
		if txErr != nil {
			continue
		}
		promisesGetRawTransaction = append(promisesGetRawTransaction, promiseGetRawTransaction{
			result: txVerbose,
			inIdx:  inIdx,
		})
	}

	addrs = make(map[string]bool)
	valsIn = make([]int64, len(msgTx.TxIn))
	// For each TxIn of this transaction, inspect the previous outpoint.
	for i := range promisesGetRawTransaction {
		// Previous outpoint for this TxIn
		inIdx := promisesGetRawTransaction[i].inIdx
		prevOut := &msgTx.TxIn[inIdx].PreviousOutPoint
		hash := prevOut.Hash

		prevTxRaw := promisesGetRawTransaction[i].result
		if prevTxRaw.Txid != hash.String() {
			fmt.Printf("TxPrevOutsByAddr error: %v != %v", prevTxRaw.Txid, hash.String())
			continue
		}

		prevTx, err := BTCMsgTxFromHex(prevTxRaw.Hex, int32(prevTxRaw.Version))
		if err != nil {
			fmt.Printf("BTC: MsgTxFromHex failed: %s\n", err)
			continue
		}

		// prevOut.Index indicates which output.
		txOut := prevTx.TxOut[prevOut.Index]

		// Get the values.
		valsIn[inIdx] = txOut.Value

		// Extract the addresses from this output's PkScript.
		_, txAddrs, _, _ := txscript.ExtractPkScriptAddrs(txOut.PkScript, params)
		if len(txAddrs) == 0 {
			fmt.Printf("pkScript of a previous transaction output "+
				"(%v:%d) unexpectedly encoded no addresses.",
				prevOut.Hash, prevOut.Index)
			continue
		}

		newPrevOuts++
		blockhash, err := chainhash.NewHashFromStr(prevTxRaw.BlockHash)
		if err != nil {
			fmt.Printf("Invalid block hash %s", prevTxRaw.BlockHash)
			continue
		}

		blockVerbose, err := WithTimeout(func() (*btcjson.GetBlockVerboseTxResult, error) {
			return c.GetBlockVerboseTx(blockhash)
		})
		if err != nil {
			fmt.Printf("Get block failed: %s", prevTxRaw.BlockHash)
			continue
		}

		// Put the previous outpoint's transaction in the txnsStore.
		txnsStore[hash] = &BTCTxWithBlockData{
			Tx:          prevTx,
			BlockHeight: blockVerbose.Height,
			BlockHash:   prevTxRaw.BlockHash,
		}

		outpoint := btcwire.NewOutPoint(&hash, prevOut.Index)
		prevOutExtended := BTCPrevOut{
			TxSpending:       msgTx.TxHash(),
			InputIndex:       inIdx,
			PreviousOutpoint: outpoint,
		}

		// For each address paid to by this previous outpoint, record the
		// previous outpoint and the containing transactions.
		for _, txAddr := range txAddrs {
			addr := txAddr.String()

			// Check if it is already in the address store.
			addrOuts := txAddrOuts[addr]
			if addrOuts == nil {
				// Insert into affected address map.
				addrs[addr] = true // new
				// Insert into the address store.
				txAddrOuts[addr] = &BTCAddressOutpoints{
					Address:  addr,
					PrevOuts: []BTCPrevOut{prevOutExtended},
				}
				continue
			}

			// Address already in the address store, append the prevout.
			addrOuts.PrevOuts = append(addrOuts.PrevOuts, prevOutExtended)

			if _, found := addrs[addr]; !found {
				addrs[addr] = false // not new to the address store
			}
		}
	}
	return
}

// Get total of BTC for tx inputs
func getBTCTotalInput(client *rpcclient.Client, tx *wire.MsgTx) (btcutil.Amount, error) {
	var totalInput btcutil.Amount

	for _, vin := range tx.TxIn {
		prevTx, err := WithTimeout(func() (*btcjson.TxRawResult, error) {
			return client.GetRawTransactionVerbose(&vin.PreviousOutPoint.Hash)
		})
		if err != nil {
			return 0, err
		}
		// Get output of prev tx (UTXO)
		prevOutput := prevTx.Vout[vin.PreviousOutPoint.Index]
		satoshis := btcutil.Amount(prevOutput.Value * 1e8) // Chuyển từ BTC -> satoshis
		totalInput += satoshis
	}

	return totalInput, nil
}

// Get total of BTC for tx outputs
func getBTCTotalOutput(tx *wire.MsgTx) btcutil.Amount {
	var totalOutput btcutil.Amount

	for _, vout := range tx.TxOut {
		totalOutput += btcutil.Amount(vout.Value)
	}

	return totalOutput
}

// Calculate tx fee from msgTx for BTC
func CalculateBTCTxFee(client *rpcclient.Client, msgTx *wire.MsgTx) (btcutil.Amount, error) {
	// Calculate input total
	totalInput, err := getBTCTotalInput(client, msgTx)
	if err != nil {
		return 0, err
	}
	// Calculate output total
	totalOutput := getBTCTotalOutput(msgTx)
	// Calculate tx fees
	fee := totalInput - totalOutput
	return fee, nil
}

func BTCTxFeeRate(msgTx *btcwire.MsgTx, client BTCVerboseTransactionGetter) (btcutil.Amount, btcutil.Amount) {
	var amtIn int64
	for _, txin := range msgTx.TxIn {
		//Get transaction
		blockhash, err := chainhash.NewHashFromStr(txin.PreviousOutPoint.Hash.String())
		if err != nil {
			continue
		}
		txResult, err := WithTimeout(func() (*btcjson.TxRawResult, error) {
			return client.GetRawTransactionVerbose(blockhash)
		})
		if err != nil {
			continue
		}
		amtIn += int64(txResult.Vout[txin.PreviousOutPoint.Index].Value)
	}
	var amtOut int64
	for iv := range msgTx.TxOut {
		amtOut += msgTx.TxOut[iv].Value
	}
	txSize := int64(msgTx.SerializeSize())
	return btcutil.Amount(amtIn - amtOut), btcutil.Amount(FeeRate(amtIn, amtOut, txSize))
}
