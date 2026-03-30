// Copyright (c) 2018-2021, The Decred developers
// Copyright (c) 2017, Jonathan Chappelow
// See LICENSE for details.

package ltcrpcutils

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ltcsuite/ltcd/btcjson"
	"github.com/ltcsuite/ltcd/chaincfg"
	"github.com/ltcsuite/ltcd/chaincfg/chainhash"
	"github.com/ltcsuite/ltcd/ltcutil"
	"github.com/ltcsuite/ltcd/rpcclient"
	"github.com/ltcsuite/ltcd/wire"

	"github.com/decred/dcrdata/v8/txhelpers"
)

type MempoolGetter interface {
	GetRawMempoolVerbose() (map[string]btcjson.GetRawMempoolVerboseResult, error)
}

type BlockGetter interface {
	GetBlockCount() (int64, error)
	GetBlockHash(blockHeight int64) (*chainhash.Hash, error)
	GetBlock(blockHash *chainhash.Hash) (*wire.MsgBlock, error)
}

type TransactionGetter interface {
	GetRawTransactionVerbose(txHash *chainhash.Hash) (*btcjson.TxRawResult, error)
}

type VerboseBlockGetter interface {
	GetBlockHash(blockHeight int64) (*chainhash.Hash, error)
	GetBlockVerbose(blockHash *chainhash.Hash) (*btcjson.GetBlockVerboseResult, error)
	GetBlockVerboseTx(blockHash *chainhash.Hash) (*btcjson.GetBlockVerboseTxResult, error)
	GetBlockHeaderVerbose(hash *chainhash.Hash) (*btcjson.GetBlockHeaderVerboseResult, error)
}

type TxWithBlockData struct {
	Tx          *wire.MsgTx
	BlockHeight int64
	BlockHash   string
	MemPoolTime int64
}

type PrevOut struct {
	TxSpending       chainhash.Hash
	InputIndex       int
	PreviousOutpoint *wire.OutPoint
}

// AsyncTxClient is a blueprint for creating a type that satisfies both
// txhelpers.VerboseTransactionPromiseGetter and
// txhelpers.TransactionPromiseGetter from an rpcclient.Client.
type AsyncTxClient struct {
	*rpcclient.Client
}

// GetRawTransactionVerbosePromise gives txhelpers.VerboseTransactionPromiseGetter.
func (cl *AsyncTxClient) GetRawTransactionVerbosePromise(txHash *chainhash.Hash) VerboseTxReceiver {
	return cl.Client.GetRawTransactionVerboseAsync(txHash)
}

var _ VerboseTransactionPromiseGetter = (*AsyncTxClient)(nil)

// GetRawTransactionPromise gives txhelpers.TransactionPromiseGetter.
func (cl *AsyncTxClient) GetRawTransactionPromise(txHash *chainhash.Hash) TxReceiver {
	return cl.Client.GetRawTransactionAsync(txHash)
}

type TxReceiver interface {
	Receive() (*ltcutil.Tx, error)
}

type TransactionPromiseGetter interface {
	GetRawTransactionPromise(txHash *chainhash.Hash) TxReceiver
}

// RawTransactionGetter is an interface satisfied by rpcclient.Client, and
// required by functions that would otherwise require a rpcclient.Client just
// for GetRawTransaction.
type RawTransactionGetter interface {
	GetRawTransaction(txHash *chainhash.Hash) (*ltcutil.Tx, error)
}

var _ TransactionPromiseGetter = (*AsyncTxClient)(nil)

// NewAsyncTxClient creates an AsyncTxClient from a rpcclient.Client.
func NewAsyncTxClient(c *rpcclient.Client) *AsyncTxClient {
	return &AsyncTxClient{c}
}

// DefaultRPCTimeout is the timeout for individual RPC calls to litecoind.
// litecoind HTTP RPC handles requests sequentially, so during block catch-up
// when many concurrent requests are queued, individual calls may take longer.
var DefaultRPCTimeout = 120 * time.Second

var (
	zeroHash = chainhash.Hash{}
	// zeroHashStringBytes = []byte(chainhash.Hash{}.String())

	maxAncestorChainLength = 8192

	ErrAncestorAtGenesis      = errors.New("no ancestor: at genesis")
	ErrAncestorMaxChainLength = errors.New("no ancestor: max chain length reached")
	ErrRPCTimeout             = errors.New("LTC RPC call timed out")
)

// WithTimeout wraps an RPC call with a timeout. If the call does not complete
// within DefaultRPCTimeout, ErrRPCTimeout is returned. This prevents the
// application from freezing when ltcd becomes unresponsive.
func WithTimeout[T any](fn func() (T, error)) (T, error) {
	type result struct {
		val T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	timer := time.NewTimer(DefaultRPCTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.val, r.err
	case <-timer.C:
		var zero T
		return zero, ErrRPCTimeout
	}
}

// ConnectNodeRPC creates an HTTP JSON-RPC connection to a litecoind node.
func ConnectNodeRPC(host, user, pass string) (*rpcclient.Client, error) {
	log.Infof("Connecting to litecoind RPC %s as user %s (HTTP mode)", host, user)

	connCfg := &rpcclient.ConnConfig{
		Host:         host,
		User:         user,
		Pass:         pass,
		HTTPPostMode: true,
		DisableTLS:   true,
	}

	client, err := rpcclient.New(connCfg, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create LTC RPC client: %w", err)
	}

	// Verify connection by getting block count
	count, err := client.GetBlockCount()
	if err != nil {
		client.Shutdown()
		return nil, fmt.Errorf("failed to connect to litecoind: %w", err)
	}

	log.Infof("Connected to litecoind at %s, block height: %d", host, count)
	return client, nil
}

// GetBestBlock returns the best block hash and height using GetBlockCount + GetBlockHash.
// This replaces the ltcd-specific GetBestBlock() RPC which is not available in litecoind.
func GetBestBlock(client *rpcclient.Client) (*chainhash.Hash, int64, error) {
	count, err := client.GetBlockCount()
	if err != nil {
		return nil, 0, fmt.Errorf("GetBlockCount failed: %w", err)
	}
	hash, err := client.GetBlockHash(count)
	if err != nil {
		return nil, 0, fmt.Errorf("GetBlockHash(%d) failed: %w", count, err)
	}
	return hash, count, nil
}

// GetBlockHeaderVerbose creates a *chainjson.GetBlockHeaderVerboseResult for the
// block at height idx via an RPC connection to a chain server.
func GetBlockHeaderVerbose(client BlockFetcher, idx int64) *btcjson.GetBlockHeaderVerboseResult {
	blockhash, err := WithTimeout(func() (*chainhash.Hash, error) {
		return client.GetBlockHash(idx)
	})
	if err != nil {
		log.Errorf("GetBlockHash(%d) failed: %v", idx, err)
		return nil
	}

	blockHeaderVerbose, err := WithTimeout(func() (*btcjson.GetBlockHeaderVerboseResult, error) {
		return client.GetBlockHeaderVerbose(blockhash)
	})
	if err != nil {
		log.Errorf("GetBlockHeaderVerbose(%v) failed: %v", blockhash, err)
		return nil
	}

	return blockHeaderVerbose
}

func GetTransactionTimeAndSize(client TransactionGetter, txid string) (int64, int64) {
	txResult, err := GetRawTransactionByTxidStr(client, txid)
	if err != nil {
		return 0, 0
	}
	return int64(txResult.Size), txResult.Time
}

func GetRawTransactionByTxidStr(client TransactionGetter, txid string) (*btcjson.TxRawResult, error) {
	txhash, err := chainhash.NewHashFromStr(txid)
	if err != nil {
		log.Errorf("Invalid transaction txid %s", txid)
		return nil, err
	}

	transactionRslt, err := WithTimeout(func() (*btcjson.TxRawResult, error) {
		return client.GetRawTransactionVerbose(txhash)
	})
	if err != nil {
		log.Errorf("GetTransaction(%v) failed: %v", txhash, err)
		return nil, err
	}

	return transactionRslt, nil
}

func GetBlockVerboseTx(client VerboseBlockGetter, idx int64) *btcjson.GetBlockVerboseTxResult {
	blockhash, err := WithTimeout(func() (*chainhash.Hash, error) {
		return client.GetBlockHash(idx)
	})
	if err != nil {
		log.Errorf("GetBlockHash(%d) failed: %v", idx, err)
		return nil
	}

	blockVerbose, err := WithTimeout(func() (*btcjson.GetBlockVerboseTxResult, error) {
		return client.GetBlockVerboseTx(blockhash)
	})
	if err != nil {
		log.Errorf("GetBlockVerboseTx(%v) failed: %v", blockhash, err)
		return nil
	}

	return blockVerbose
}

func GetBlockVerboseTxByHash(client VerboseBlockGetter, hash string) *btcjson.GetBlockVerboseTxResult {
	blockhash, err := chainhash.NewHashFromStr(hash)
	if err != nil {
		log.Errorf("Invalid block hash %s", hash)
		return nil
	}

	blockVerbose, err := WithTimeout(func() (*btcjson.GetBlockVerboseTxResult, error) {
		return client.GetBlockVerboseTx(blockhash)
	})
	if err != nil {
		log.Errorf("GetBlockVerbose(%v) failed: %v", blockhash, err)
		return nil
	}

	return blockVerbose
}

// GetBlockHeaderVerboseByString creates a *chainjson.GetBlockHeaderVerboseResult
// for the block specified by hash via an RPC connection to a chain server.
func GetBlockHeaderVerboseByString(client BlockFetcher, hash string) *btcjson.GetBlockHeaderVerboseResult {
	blockhash, err := chainhash.NewHashFromStr(hash)
	if err != nil {
		log.Errorf("Invalid block hash %s: %v", blockhash, err)
		return nil
	}

	blockHeaderVerbose, err := WithTimeout(func() (*btcjson.GetBlockHeaderVerboseResult, error) {
		return client.GetBlockHeaderVerbose(blockhash)
	})
	if err != nil {
		log.Errorf("GetBlockHeaderVerbose(%v) failed: %v", blockhash, err)
		return nil
	}

	return blockHeaderVerbose
}

func IsValidBlockHash(client BlockFetcher, hash string) bool {
	blockhash, err := chainhash.NewHashFromStr(hash)
	if err != nil {
		return false
	}

	_, err = WithTimeout(func() (*btcjson.GetBlockHeaderVerboseResult, error) {
		return client.GetBlockHeaderVerbose(blockhash)
	})
	return err == nil
}

func IsValidTxHash(client TransactionGetter, hash string) bool {
	txhash, err := chainhash.NewHashFromStr(hash)
	if err != nil {
		return false
	}
	_, err = WithTimeout(func() (*btcjson.TxRawResult, error) {
		return client.GetRawTransactionVerbose(txhash)
	})
	return err == nil
}

// GetBlockVerbose creates a *chainjson.GetBlockVerboseResult for the block index
// specified by idx via an RPC connection to a chain server.
func GetBlockVerbose(client VerboseBlockGetter, idx int64) *btcjson.GetBlockVerboseResult {
	blockhash, err := WithTimeout(func() (*chainhash.Hash, error) {
		return client.GetBlockHash(idx)
	})
	if err != nil {
		log.Errorf("GetBlockHash(%d) failed: %v", idx, err)
		return nil
	}

	blockVerbose, err := WithTimeout(func() (*btcjson.GetBlockVerboseResult, error) {
		return client.GetBlockVerbose(blockhash)
	})
	if err != nil {
		log.Errorf("GetBlockVerbose(%v) failed: %v", blockhash, err)
		return nil
	}

	return blockVerbose
}

func GetBlockchainDifficulty(client BlockchainGetter) (float64, error) {
	blockchainInfo, err := WithTimeout(func() (*btcjson.GetBlockChainInfoResult, error) {
		return client.GetBlockChainInfo()
	})
	if err != nil {
		return 0, err
	}
	return blockchainInfo.Difficulty, nil
}

// GetBlockVerboseByHash creates a *chainjson.GetBlockVerboseResult for the
// specified block hash via an RPC connection to a chain server.
func GetBlockVerboseByHash(client VerboseBlockGetter, hash string) *btcjson.GetBlockVerboseResult {
	blockhash, err := chainhash.NewHashFromStr(hash)
	if err != nil {
		log.Errorf("Invalid block hash %s", hash)
		return nil
	}

	blockVerbose, err := WithTimeout(func() (*btcjson.GetBlockVerboseResult, error) {
		return client.GetBlockVerbose(blockhash)
	})
	if err != nil {
		log.Errorf("GetBlockVerbose(%v) failed: %v", blockhash, err)
		return nil
	}

	return blockVerbose
}

// GetBlock gets a block at the given height from a chain server.
func GetBlock(ind int64, client BlockFetcher) (*ltcutil.Block, *chainhash.Hash, error) {
	blockhash, err := WithTimeout(func() (*chainhash.Hash, error) {
		return client.GetBlockHash(ind)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("GetBlockHash(%d) failed: %v", ind, err)
	}

	msgBlock, err := WithTimeout(func() (*wire.MsgBlock, error) {
		return client.GetBlock(blockhash)
	})
	if err != nil {
		return nil, blockhash,
			fmt.Errorf("GetBlock failed (%s): %v", blockhash, err)
	}
	block := ltcutil.NewBlock(msgBlock)

	return block, blockhash, nil
}

// GetBlockByHash gets the block with the given hash from a chain server.
func GetBlockByHash(blockhash *chainhash.Hash, client BlockFetcher) (*ltcutil.Block, error) {
	msgBlock, err := WithTimeout(func() (*wire.MsgBlock, error) {
		return client.GetBlock(blockhash)
	})
	if err != nil {
		return nil, fmt.Errorf("GetBlock failed (%s): %v", blockhash, err)
	}
	block := ltcutil.NewBlock(msgBlock)

	return block, nil
}

// SideChainFull gets all of the blocks in the side chain with the specified tip
// block hash. The first block in the slice is the lowest height block in the
// side chain, and its previous block is the main/side common ancestor, which is
// not included in the slice since it is main chain. The last block in the slice
// is thus the side chain tip.
func SideChainFull(client BlockFetcher, tipHash string) ([]string, error) {
	// Do not assume specified tip hash is even side chain.
	var sideChain []string

	hash := tipHash
	for {
		header := GetBlockHeaderVerboseByString(client, hash)
		if header == nil {
			return nil, fmt.Errorf("GetBlockHeaderVerboseByString failed for block %s", hash)
		}

		// Main chain blocks have Confirmations != -1.
		if header.Confirmations != -1 {
			// The passed block is main chain, not a side chain tip.
			if hash == tipHash {
				return nil, fmt.Errorf("tip block is not on a side chain")
			}
			// This previous block is the main/side common ancestor.
			break
		}

		// This was another side chain block.
		sideChain = append(sideChain, hash)

		// On to previous block
		hash = header.PreviousHash
	}

	// Reverse side chain order so that last element is tip.
	reverseStringSlice(sideChain)

	return sideChain, nil
}

func reverseStringSlice(s []string) {
	N := len(s)
	for i := 0; i <= (N/2)-1; i++ {
		j := N - 1 - i
		s[i], s[j] = s[j], s[i]
	}
}

// BlockHashGetter is an interface implementing GetBlockHash to retrieve a block
// hash from a height.
type BlockHashGetter interface {
	GetBlockHash(context.Context, int64) (*chainhash.Hash, error)
}

// OrphanedTipLength finds a common ancestor by iterating block heights
// backwards until a common block hash is found. Unlike CommonAncestor, an
// orphaned DB tip whose corresponding block is not known to dcrd will not cause
// an error. The number of blocks that have been orphaned is returned.
// Realistically, this should rarely be anything but 0 or 1, but no limits are
// placed here on the number of blocks checked.
func OrphanedTipLength(ctx context.Context, client BlockHashGetter,
	tipHeight int64, hashFunc func(int64) (string, error)) (int64, error) {
	commonHeight := tipHeight
	var dbHash string
	var err error
	var dcrdHash *chainhash.Hash
	for {
		// Since there are no limits on the number of blocks scanned, allow
		// cancellation for a clean exit.
		select {
		case <-ctx.Done():
			return 0, nil
		default:
		}

		dbHash, err = hashFunc(commonHeight)
		if err != nil {
			return -1, fmt.Errorf("Unable to retrieve block at height %d: %v", commonHeight, err)
		}
		dcrdHash, err = WithTimeout(func() (*chainhash.Hash, error) {
			return client.GetBlockHash(ctx, commonHeight)
		})
		if err != nil {
			return -1, fmt.Errorf("Unable to retrieve dcrd block at height %d: %v", commonHeight, err)
		}
		if dcrdHash.String() == dbHash {
			break
		}

		commonHeight--
		if commonHeight < 0 {
			return -1, fmt.Errorf("Unable to find a common ancestor")
		}
		// Reorgs are soft-limited to depth 6 by dcrd. More than six blocks without
		// a match probably indicates an issue.
		if commonHeight-tipHeight == 7 {
			log.Warnf("No common ancestor within 6 blocks. This is abnormal")
		}
	}
	return tipHeight - commonHeight, nil
}

// MempoolAddressChecker is an interface implementing UnconfirmedTxnsForAddress.
// NewMempoolAddressChecker may be used to create a MempoolAddressChecker from
// an rpcclient.Client.
type MempoolAddressChecker interface {
	UnconfirmedTxnsForAddress(address string) (*txhelpers.LTCAddressOutpoints, int64, error)
}

type mempoolAddressChecker struct {
	client *AsyncTxClient
	params *chaincfg.Params
}

type VerboseTxReceiver interface {
	Receive() (*btcjson.TxRawResult, error)
}

type VerboseTransactionPromiseGetter interface {
	GetRawTransactionVerbosePromise(txHash *chainhash.Hash) VerboseTxReceiver
}

// UnconfirmedTxnsForAddress implements MempoolAddressChecker.
func (m *mempoolAddressChecker) UnconfirmedTxnsForAddress(address string) (*txhelpers.LTCAddressOutpoints, int64, error) {
	return UnconfirmedTxnsForAddress(m.client, address, m.params)
}

// NewMempoolAddressChecker creates a new MempoolAddressChecker from an RPC
// client for the given network.
func NewMempoolAddressChecker(client *rpcclient.Client, params *chaincfg.Params) MempoolAddressChecker {
	return &mempoolAddressChecker{&AsyncTxClient{client}, params}
}

func NewAddressOutpoints(address string) *txhelpers.LTCAddressOutpoints {
	return &txhelpers.LTCAddressOutpoints{
		Address:   address,
		TxnsStore: make(map[chainhash.Hash]*txhelpers.LTCTxWithBlockData),
	}
}

// MempoolTxGetter must be satisfied for UnconfirmedTxnsForAddress.
type MempoolTxGetter interface {
	MempoolGetter
	RawTransactionGetter
	VerboseTransactionPromiseGetter
	GetBlockCount() (int64, error)
}

type BlockchainGetter interface {
	GetBlockChainInfo() (*btcjson.GetBlockChainInfoResult, error)
}

// UnconfirmedTxnsForAddress returns the chainhash.Hash of all transactions in
// mempool that (1) pay to the given address, or (2) spend a previous outpoint
// that paid to the address.
func UnconfirmedTxnsForAddress(client MempoolTxGetter, address string,
	params *chaincfg.Params) (*txhelpers.LTCAddressOutpoints, int64, error) {
	return nil, 0, nil
}
