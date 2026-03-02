package txhelpers

import (
	"encoding/hex"
	"fmt"
	"strings"

	ltcjson "github.com/ltcsuite/ltcd/btcjson"
	ltcchaincfg "github.com/ltcsuite/ltcd/chaincfg"
	"github.com/ltcsuite/ltcd/chaincfg/chainhash"
	"github.com/ltcsuite/ltcd/ltcutil"
	"github.com/ltcsuite/ltcd/rpcclient"
	"github.com/ltcsuite/ltcd/txscript"
	ltctxscript "github.com/ltcsuite/ltcd/txscript"
	"github.com/ltcsuite/ltcd/wire"
	ltcwire "github.com/ltcsuite/ltcd/wire"
)

var (
	ltcZeroHash = chainhash.Hash{}
)

type LTCAddressOutpoints struct {
	Address   string
	Outpoints []*ltcwire.OutPoint
	PrevOuts  []LTCPrevOut
	TxnsStore map[chainhash.Hash]*LTCTxWithBlockData
}

func NewLTCAddressOutpoints(address string) *LTCAddressOutpoints {
	return &LTCAddressOutpoints{
		Address:   address,
		TxnsStore: make(map[chainhash.Hash]*LTCTxWithBlockData),
	}
}

type LTCPrevOut struct {
	TxSpending       chainhash.Hash
	InputIndex       int
	PreviousOutpoint *ltcwire.OutPoint
}

type LTCRawTransactionGetter interface {
	GetRawTransaction(txHash *chainhash.Hash) (*ltcutil.Tx, error)
}

type LTCVerboseTransactionGetter interface {
	GetRawTransactionVerbose(txHash *chainhash.Hash) (*ltcjson.TxRawResult, error)
	GetBlockVerboseTx(blockHash *chainhash.Hash) (*ltcjson.GetBlockVerboseTxResult, error)
}

func LTCMsgTxFromHex(txhex string, version int32) (*ltcwire.MsgTx, error) {
	msgTx := ltcwire.NewMsgTx(version)
	if err := msgTx.Deserialize(hex.NewDecoder(strings.NewReader(txhex))); err != nil {
		return nil, err
	}
	return msgTx, nil
}

func TotalLTCVout(vouts []ltcjson.Vout) ltcutil.Amount {
	var total ltcutil.Amount
	for _, v := range vouts {
		a, err := ltcutil.NewAmount(v.Value)
		if err != nil {
			continue
		}
		total += a
	}
	return total
}

// IsZeroHash checks if the Hash is the zero hash.
func IsLTCZeroHash(hash chainhash.Hash) bool {
	return hash == ltcZeroHash
}

func MsgLTCTxFromHex(txhex string, version int32) (*ltcwire.MsgTx, error) {
	msgTx := ltcwire.NewMsgTx(version)
	if err := msgTx.Deserialize(hex.NewDecoder(strings.NewReader(txhex))); err != nil {
		return nil, err
	}
	return msgTx, nil
}

func LTCOutPointAddresses(outPoint *ltcwire.OutPoint, c LTCRawTransactionGetter,
	params *ltcchaincfg.Params) ([]string, ltcutil.Amount, error) {
	// The addresses are encoded in the pkScript, so we need to get the
	// raw transaction, and the TxOut that contains the pkScript.
	prevTx, err := WithTimeout(func() (*ltcutil.Tx, error) {
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
	_, txAddresses, _, err := ltctxscript.ExtractPkScriptAddrs(txOut.PkScript, params)
	if err != nil {
		return nil, 0, fmt.Errorf("Invalid tx hash get address")
	}
	value := ltcutil.Amount(txOut.Value)
	addresses := make([]string, 0, len(txAddresses))
	for _, txAddr := range txAddresses {
		addr := txAddr.String()
		addresses = append(addresses, addr)
	}
	return addresses, value, nil
}

type LTCTxnsStore map[chainhash.Hash]*LTCTxWithBlockData

type LTCTxWithBlockData struct {
	Tx          *ltcwire.MsgTx
	BlockHeight int64
	BlockHash   string
	MemPoolTime int64
}

// MempoolAddressStore organizes AddressOutpoints by address.
type LTCMempoolAddressStore map[string]*LTCAddressOutpoints

func LTCTxOutpointsByAddr(txAddrOuts LTCMempoolAddressStore, msgTx *ltcwire.MsgTx, params *ltcchaincfg.Params) (newOuts int, addrs map[string]bool) {
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

			op := ltcwire.NewOutPoint(txHash, uint32(outIndex))

			addrOuts := txAddrOuts[addr]
			if addrOuts == nil {
				addrOuts = &LTCAddressOutpoints{
					Address:   addr,
					Outpoints: []*ltcwire.OutPoint{op},
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
func LTCTotalOutFromMsgTx(msgTx *ltcwire.MsgTx) ltcutil.Amount {
	var amtOut int64
	for _, v := range msgTx.TxOut {
		amtOut += v.Value
	}
	return ltcutil.Amount(amtOut)
}

func LTCTxPrevOutsByAddr(txAddrOuts LTCMempoolAddressStore, txnsStore LTCTxnsStore, msgTx *ltcwire.MsgTx, c LTCVerboseTransactionGetter,
	params *ltcchaincfg.Params) (newPrevOuts int, addrs map[string]bool, valsIn []int64) {
	if txAddrOuts == nil {
		panic("LTCTxPrevOutsByAddr: input map must be initialized: map[string]*AddressOutpoints")
	}
	if txnsStore == nil {
		panic("LTCTxPrevOutsByAddr: input map must be initialized: map[string]*AddressOutpoints")
	}

	// Send all the raw transaction requests
	type promiseGetRawTransaction struct {
		result *ltcjson.TxRawResult
		inIdx  int
	}
	promisesGetRawTransaction := make([]promiseGetRawTransaction, 0, len(msgTx.TxIn))

	for inIdx, txIn := range msgTx.TxIn {
		hash := &txIn.PreviousOutPoint.Hash
		if ltcZeroHash.IsEqual(hash) {
			continue // coinbase or stakebase
		}
		txVerbose, txErr := WithTimeout(func() (*ltcjson.TxRawResult, error) {
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

		prevTx, err := LTCMsgTxFromHex(prevTxRaw.Hex, int32(prevTxRaw.Version))
		if err != nil {
			fmt.Printf("TxPrevOutsByAddr: MsgTxFromHex failed: %s\n", err)
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

		blockVerbose, err := WithTimeout(func() (*ltcjson.GetBlockVerboseTxResult, error) {
			return c.GetBlockVerboseTx(blockhash)
		})
		if err != nil {
			fmt.Printf("Get block failed: %s", prevTxRaw.BlockHash)
			continue
		}

		// Put the previous outpoint's transaction in the txnsStore.
		txnsStore[hash] = &LTCTxWithBlockData{
			Tx:          prevTx,
			BlockHeight: blockVerbose.Height,
			BlockHash:   prevTxRaw.BlockHash,
		}

		outpoint := ltcwire.NewOutPoint(&hash, prevOut.Index)
		prevOutExtended := LTCPrevOut{
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
				txAddrOuts[addr] = &LTCAddressOutpoints{
					Address:  addr,
					PrevOuts: []LTCPrevOut{prevOutExtended},
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

func LTCTxFeeRate(msgTx *ltcwire.MsgTx, client LTCVerboseTransactionGetter) (ltcutil.Amount, ltcutil.Amount) {
	var amtIn int64
	for _, txin := range msgTx.TxIn {
		//Get transaction
		blockhash, err := chainhash.NewHashFromStr(txin.PreviousOutPoint.Hash.String())
		if err != nil {
			continue
		}
		txResult, err := WithTimeout(func() (*ltcjson.TxRawResult, error) {
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
	return ltcutil.Amount(amtIn - amtOut), ltcutil.Amount(FeeRate(amtIn, amtOut, txSize))
}

// Get total of BTC for tx inputs
func getLTCTotalInput(client *rpcclient.Client, tx *wire.MsgTx) (ltcutil.Amount, error) {
	var totalInput ltcutil.Amount

	for _, vin := range tx.TxIn {
		prevTx, err := WithTimeout(func() (*ltcjson.TxRawResult, error) {
			return client.GetRawTransactionVerbose(&vin.PreviousOutPoint.Hash)
		})
		if err != nil {
			return 0, err
		}
		// Get output of prev tx (UTXO)
		prevOutput := prevTx.Vout[vin.PreviousOutPoint.Index]
		satoshis := ltcutil.Amount(prevOutput.Value * 1e8) // Chuyển từ BTC -> satoshis
		totalInput += satoshis
	}

	return totalInput, nil
}

// Get total of BTC for tx outputs
func getLTCTotalOutput(tx *wire.MsgTx) ltcutil.Amount {
	var totalOutput ltcutil.Amount

	for _, vout := range tx.TxOut {
		totalOutput += ltcutil.Amount(vout.Value)
	}

	return totalOutput
}

// Calculate tx fee from msgTx for BTC
func CalculateLTCTxFee(client *rpcclient.Client, msgTx *wire.MsgTx) (ltcutil.Amount, error) {
	// Calculate input total
	totalInput, err := getLTCTotalInput(client, msgTx)
	if err != nil {
		return 0, err
	}
	// Calculate output total
	totalOutput := getLTCTotalOutput(msgTx)
	// Calculate tx fees
	fee := totalInput - totalOutput
	return fee, nil
}
