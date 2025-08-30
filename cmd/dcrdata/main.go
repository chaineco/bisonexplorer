// Copyright (c) 2018-2022, The Decred developers
// Copyright (c) 2017, Jonathan Chappelow
// See LICENSE for details.

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"time"

	btcchainhash "github.com/btcsuite/btcd/chaincfg/chainhash"
	btcClient "github.com/btcsuite/btcd/rpcclient"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrdata/db/dcrpg/v8"
	"github.com/decred/dcrdata/exchanges/v3"
	"github.com/decred/dcrdata/gov/v6/agendas"
	politeia "github.com/decred/dcrdata/gov/v6/politeia"
	"github.com/decred/dcrdata/v8/blockdata"
	"github.com/decred/dcrdata/v8/blockdata/blockdatabtc"
	"github.com/decred/dcrdata/v8/blockdata/blockdataltc"
	"github.com/decred/dcrdata/v8/db/cache"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/mempool"
	"github.com/decred/dcrdata/v8/mempool/mempoolltc"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/mutilchain/btcrpcutils"
	"github.com/decred/dcrdata/v8/mutilchain/externalapi"
	"github.com/decred/dcrdata/v8/mutilchain/ltcrpcutils"
	"github.com/decred/dcrdata/v8/pubsub"
	pstypes "github.com/decred/dcrdata/v8/pubsub/types"
	"github.com/decred/dcrdata/v8/rpcutils"
	"github.com/decred/dcrdata/v8/semver"
	"github.com/decred/dcrdata/v8/stakedb"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/gops/agent"
	ltcchainhash "github.com/ltcsuite/ltcd/chaincfg/chainhash"
	ltcClient "github.com/ltcsuite/ltcd/rpcclient"

	"github.com/decred/dcrdata/cmd/dcrdata/internal/api"
	"github.com/decred/dcrdata/cmd/dcrdata/internal/api/insight"
	"github.com/decred/dcrdata/cmd/dcrdata/internal/chainsocket"
	"github.com/decred/dcrdata/cmd/dcrdata/internal/explorer"
	mw "github.com/decred/dcrdata/cmd/dcrdata/internal/middleware"
	notify "github.com/decred/dcrdata/cmd/dcrdata/internal/notification"
)

func main() {
	// Create a context that is cancelled when a shutdown request is received
	// via requestShutdown.
	ctx := withShutdownCancel(context.Background())
	// Listen for both interrupt signals and shutdown requests.
	go shutdownListener()

	if err := _main(ctx); err != nil {
		if infoRotator != nil || debugRotator != nil {
			log.Error(err)
		}
		os.Exit(1)
	}
	os.Exit(0)
}

// Instead of an rpcutils.AsyncTxClient for NewMempoolDataCollector, we could
// make a simple wrapper to provide txhelpers.VerboseTransactionPromiseGetter:
//
// type mempoolClient struct {
// 	*rpcclient.Client
// }
// func (cl *mempoolClient) GetRawTransactionVerbosePromise(ctx context.Context, txHash *chainhash.Hash) txhelpers.VerboseTxReceiver {
// 	return cl.Client.GetRawTransactionVerboseAsync(ctx, txHash)
// }
// var _ txhelpers.VerboseTransactionPromiseGetter = (*mempoolClient)(nil)

// _main does all the work. Deferred functions do not run after os.Exit(), so
// main wraps this function, which returns a code.
func _main(ctx context.Context) error {
	// Parse the configuration file, and setup logger.
	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Failed to load dcrdata config: %s\n", err.Error())
		return err
	}
	defer closeLogRotators()

	if cfg.CPUProfile != "" {
		var f *os.File
		f, err = os.Create(cfg.CPUProfile)
		if err != nil {
			return err
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	if cfg.UseGops {
		// Start gops diagnostic agent, with shutdown cleanup.
		if err = agent.Listen(agent.Options{}); err != nil {
			return err
		}
		defer agent.Close()
	}
	// Display app version.
	log.Infof("%s version %v (Go version %s)", AppName, Version(), runtime.Version())

	// Grab a Notifier. After all databases are synced, register handlers with
	// the Register*Group methods, set the best block height with
	// SetPreviousBlock and start receiving notifications with Listen. Create
	// the notifier now so the *rpcclient.NotificationHandlers can be obtained,
	// using (*Notifier).DcrdHandlers, for the rpcclient.Client constructor.
	notifier := notify.NewNotifier()
	// Connect to dcrd RPC server using a websocket.
	dcrdClient, nodeVer, err := connectNodeRPC(cfg, notifier.DcrdHandlers())
	if err != nil || dcrdClient == nil {
		return fmt.Errorf("Connection to dcrd failed: %v", err)
	}

	// Display connected network (e.g. mainnet, testnet, simnet).
	curnet, err := dcrdClient.GetCurrentNet(ctx)
	if err != nil {
		return fmt.Errorf("Unable to get current network from dcrd: %v", err)
	}

	log.Infof("Connected to dcrd (JSON-RPC API v%s) on %v",
		nodeVer.String(), curnet.String())

	if curnet != activeNet.Net {
		log.Criticalf("DCRD: Network of connected node, %s, does not match expected "+
			"network, %s.", activeNet.Net, curnet)
		return fmt.Errorf("expected network %s, got %s", activeNet.Net, curnet)
	}

	// Wrap the rpcclient to satisfy the TransactionPromiseGetter and
	// VerboseTransactionPromiseGetter interfaces in txhelpers. Both stakedb and
	// mempool packages use this rather than require an actual rpcclient.Client.
	promiseClient := rpcutils.NewAsyncTxClient(dcrdClient)

	// StakeDatabase
	stakeDB, stakeDBHeight, err := stakedb.NewStakeDatabase(promiseClient, activeChain, cfg.DataDir)
	if err != nil {
		log.Errorf("Unable to create stake DB: %v", err)
		if stakeDBHeight >= 0 {
			log.Infof("Attempting to recover stake DB...")
			stakeDB, err = stakedb.LoadAndRecover(promiseClient, activeChain, cfg.DataDir, stakeDBHeight-288)
			stakeDBHeight = int64(stakeDB.Height())
		}
		if err != nil {
			if stakeDB != nil {
				_ = stakeDB.Close()
			}
			return fmt.Errorf("StakeDatabase recovery failed: %v", err)
		}
	}
	defer stakeDB.Close()

	log.Infof("Loaded StakeDatabase at height %d", stakeDBHeight)

	// Main chain DB
	var newPGIndexes, updateAllAddresses bool
	pgHost, pgPort := cfg.PGHost, ""
	if !strings.HasPrefix(pgHost, "/") {
		pgHost, pgPort, err = net.SplitHostPort(cfg.PGHost)
		if err != nil {
			return fmt.Errorf("SplitHostPort failed: %v", err)
		}
	}
	dbi := dcrpg.DBInfo{
		Host:         pgHost,
		Port:         pgPort,
		User:         cfg.PGUser,
		Pass:         cfg.PGPass,
		DBName:       cfg.PGDBName,
		QueryTimeout: cfg.PGQueryTimeout,
	}

	// If using {netname} then replace it with activeNet.Name.
	dbi.DBName = strings.Replace(dbi.DBName, "{netname}", activeNet.Name, -1)

	// Rough estimate of capacity in rows, using size of struct plus some
	// for the string buffer of the Address field.
	rowCap := cfg.AddrCacheCap / int(32+reflect.TypeOf(dbtypes.AddressRowCompact{}).Size())
	log.Infof("Address cache capacity: %d addresses: ~%.0f MiB tx data (%d items) + %.0f MiB UTXOs",
		cfg.AddrCacheLimit, float64(cfg.AddrCacheCap)/1024/1024, rowCap, float64(cfg.AddrCacheUXTOCap)/1024/1024)

	// Open and upgrade the database.
	dbCfg := dcrpg.ChainDBCfg{
		DBi:                  &dbi,
		Params:               activeChain,
		LTCParams:            ltcActiveChain,
		BTCParams:            btcActiveChain,
		DevPrefetch:          !cfg.NoDevPrefetch,
		HidePGConfig:         cfg.HidePGConfig,
		AddrCacheAddrCap:     cfg.AddrCacheLimit,
		AddrCacheRowCap:      rowCap,
		AddrCacheUTXOByteCap: cfg.AddrCacheUXTOCap,
		ChainDBDisabled:      cfg.DisableChainDB,
		OkLinkAPIKey:         cfg.OkLinkKey,
	}

	mpChecker := rpcutils.NewMempoolAddressChecker(dcrdClient, activeChain)
	chainDB, err := dcrpg.NewChainDB(ctx, &dbCfg,
		stakeDB, mpChecker, dcrdClient, requestShutdown)
	if chainDB != nil {
		defer chainDB.Close()
	}
	if err != nil {
		return fmt.Errorf("Failed to connect to PostgreSQL: %w", err)
	}

	if cfg.DropIndexes {
		log.Info("Dropping all table indexing and quitting...")
		err = chainDB.DeindexAll()
		requestShutdown()
		return err
	}

	tspendExist, err := chainDB.CheckTableExist(dcrpg.TSpentVotesTable)
	if err != nil {
		return err
	}
	if !tspendExist {
		createTSpendVotesTableErr := chainDB.CheckCreateTSpendVotesTable()
		if createTSpendVotesTableErr != nil {
			return fmt.Errorf("create %s table failed: %w", dcrpg.TSpentVotesTable, createTSpendVotesTableErr)
		}
	}

	// check btc swaps table and create
	btcSwapsExist, err := chainDB.CheckTableExist(dcrpg.BtcSwapsTable)
	if err != nil {
		return err
	}
	if !btcSwapsExist {
		createBtcSwapsTableErr := chainDB.CheckCreateBtcSwapsTable()
		if createBtcSwapsTableErr != nil {
			return fmt.Errorf("create %s table failed: %w", dcrpg.BtcSwapsTable, createBtcSwapsTableErr)
		}
	}

	// Check for missing indexes.
	missingIndexes, descs, err := chainDB.MissingIndexes()
	if err != nil {
		return err
	}

	// If any indexes are missing, forcibly drop any existing indexes, and
	// create them all after block sync.
	if len(missingIndexes) > 0 {
		newPGIndexes = true
		updateAllAddresses = true
		// Warn if this is not a fresh sync.
		if chainDB.Height() > 0 {
			log.Warnf("Some table indexes not found!")
			for im, mi := range missingIndexes {
				log.Warnf(` - Missing Index "%s": "%s"`, mi, descs[im])
			}
			log.Warnf("Forcing new index creation and addresses table spending info update.")
		}
	}

	//Create 24h blocks table
	create24hBlocksErr := chainDB.CheckCreate24hBlocksTable()
	if create24hBlocksErr != nil {
		return fmt.Errorf("Check and create 24hblocks table failed: %w", create24hBlocksErr)
	}

	var barLoad chan *dbtypes.ProgressBarLoad
	var ltcdClient *ltcClient.Client
	var btcdClient *btcClient.Client
	var ltcNotifier *notify.LTCNotifier
	var btcNotifier *notify.BTCNotifier
	var ltcHeight int32
	var btcHeight int32
	var ltcDisabled = mutilchain.IsDisabledChain(cfg.DisabledChain, mutilchain.TYPELTC)
	var btcDisabled = mutilchain.IsDisabledChain(cfg.DisabledChain, mutilchain.TYPEBTC)
	var dcrDisabled = mutilchain.IsDisabledChain(cfg.DisabledChain, mutilchain.TYPEDCR)
	chainDB.ChainDisabledMap[mutilchain.TYPEBTC] = btcDisabled
	chainDB.ChainDisabledMap[mutilchain.TYPELTC] = ltcDisabled
	chainDB.ChainDisabledMap[mutilchain.TYPEDCR] = dcrDisabled

	//init mutilchain rpc client and set to chainDB
	if !ltcDisabled {
		//Start create rpcclient
		ltcNotifier = notify.NewLtcNotifier()
		var ltcNodeVer semver.Semver
		var ltcConnectErr error
		ltcdClient, ltcNodeVer, ltcConnectErr = connectLTCNodeRPC(cfg, ltcNotifier.LtcdHandlers())
		if ltcConnectErr != nil || ltcdClient == nil {
			return fmt.Errorf("Connection to ltcd failed: %v", ltcConnectErr)
		}
		ltcCurnet, ltcErr := ltcdClient.GetCurrentNet()
		if ltcErr != nil {
			return fmt.Errorf("Unable to get current network from ltcd: %v", ltcErr)
		}
		chainDB.LtcClient = ltcdClient
		log.Infof("Connected to ltcd (JSON-RPC API v%s) on %v", ltcNodeVer.String(), ltcCurnet.String())

		if ltcCurnet != ltcActiveNet.Net {
			log.Criticalf("LTCD: Network of connected node, %s, does not match expected "+
				"network, %s.", ltcActiveNet.Net, ltcCurnet)
			return fmt.Errorf("expected network %s, got %s", ltcActiveNet.Net, ltcCurnet)
		}

		var ltcHash *ltcchainhash.Hash
		ltcHash, ltcHeight, err = ltcdClient.GetBestBlock()
		ltcTime := int64(0)
		if err != nil {
			return fmt.Errorf("Unable to get block from ltc node: %v", err)
		}
		blockhash, err := ltcdClient.GetBlockHash(int64(ltcHeight))
		if err == nil {
			blockRst, rstErr := ltcdClient.GetBlockVerbose(blockhash)
			if rstErr == nil {
				ltcTime = blockRst.Time
			}
		}
		//create bestblock object
		bestBlock := &dcrpg.MutilchainBestBlock{
			Height: int64(ltcHeight),
			Hash:   ltcHash.String(),
			Time:   ltcTime,
		}
		chainDB.LtcBestBlock = bestBlock
	}

	if !btcDisabled {
		//Start create rpcclient
		btcNotifier = notify.NewBtcNotifier()
		var btcNodeVer semver.Semver
		var btcConnectErr error
		btcdClient, btcNodeVer, btcConnectErr = connectBTCNodeRPC(cfg, btcNotifier.BtcdHandlers())
		if btcConnectErr != nil || btcdClient == nil {
			return fmt.Errorf("Connection to btcd failed: %v", btcConnectErr)
		}
		btcCurnet, btcErr := btcdClient.GetCurrentNet()
		if btcErr != nil {
			return fmt.Errorf("Unable to get current network from btcd: %v", btcErr)
		}
		log.Infof("Connected to btcd (JSON-RPC API v%s) on %v", btcNodeVer.String(), btcCurnet.String())
		if btcCurnet != btcActiveNet.Net {
			log.Criticalf("BTCD: Network of connected node, %s, does not match expected "+
				"network, %s.", btcActiveNet.Net, btcCurnet)
			return fmt.Errorf("expected network %s, got %s", btcActiveNet.Net, btcCurnet)
		}
		chainDB.BtcClient = btcdClient

		var btcHash *btcchainhash.Hash
		btcHash, btcHeight, err = btcdClient.GetBestBlock()
		btcTime := int64(0)
		if err != nil {
			return fmt.Errorf("Unable to get block from btc node: %v", err)
		}
		blockhash, err := btcdClient.GetBlockHash(int64(btcHeight))
		if err == nil {
			blockRst, rstErr := btcdClient.GetBlockVerbose(blockhash)
			if rstErr == nil {
				btcTime = blockRst.Time
			}
		}
		//create bestblock object
		bestBlock := &dcrpg.MutilchainBestBlock{
			Height: int64(btcHeight),
			Hash:   btcHash.String(),
			Time:   btcTime,
		}
		chainDB.BtcBestBlock = bestBlock
	}

	defer func() {
		if dcrdClient != nil {
			log.Infof("Closing connection to dcrd.")
			dcrdClient.Shutdown()
			dcrdClient.WaitForShutdown()
		}

		if ltcdClient != nil {
			log.Infof("Closing connection to ltcd.")
			ltcdClient.Shutdown()
			ltcdClient.WaitForShutdown()
		}

		if btcdClient != nil {
			log.Infof("Closing connection to btcd.")
			btcdClient.Shutdown()
			btcdClient.WaitForShutdown()
		}
		log.Infof("Bye!")
		time.Sleep(250 * time.Millisecond)
	}()

	// Heights gets the current height of the DB and the chain server.
	Heights := func() (nodeHeight, chainDBHeight int64, err error) {
		_, nodeHeight, err = dcrdClient.GetBestBlock(ctx)
		if err != nil {
			err = fmt.Errorf("unable to get block from node: %w", err)
			return
		}

		chainDBHeight, err = chainDB.HeightDB()
		if err != nil {
			err = fmt.Errorf("chainDB.HeightDB failed: %w", err)
			return
		}
		if chainDBHeight == -1 {
			log.Infof("chainDB block summary table is empty.")
		}
		log.Debugf("chainDB height: %d", chainDBHeight)

		return
	}

	// Check for database tip blocks that have been orphaned. If any are found,
	// purge blocks to get to a common ancestor. Only message when purging more
	// than requested in the configuration settings.
	blocksToPurge := int64(cfg.PurgeNBestBlocks)
	_, chainDBHeight, err := Heights()
	if err != nil {
		return fmt.Errorf("Failed to get Heights for tip check: %w", err)
	}

	if chainDBHeight > -1 {
		orphaned, err := rpcutils.OrphanedTipLength(ctx, dcrdClient, chainDBHeight, chainDB.BlockHash)
		if err != nil {
			return fmt.Errorf("Failed to compare tip blocks for the DB: %w", err)
		}
		if orphaned > blocksToPurge {
			blocksToPurge = orphaned
			log.Infof("Orphaned tip detected in DB. Purging %d blocks", blocksToPurge)
		}
	}

	// Give a chance to abort a purge.
	if shutdownRequested(ctx) {
		return nil
	}

	if blocksToPurge > 0 {
		purgeToBlock := chainDBHeight - blocksToPurge
		log.Infof("Purging PostgreSQL data for the %d best blocks back to %d...", blocksToPurge, purgeToBlock)
		s, heightDB, err := chainDB.PurgeBestBlocks(blocksToPurge)
		if err != nil {
			return fmt.Errorf("failed to purge %d blocks from PostgreSQL: %w", blocksToPurge, err)
		}
		if s != nil {
			log.Infof("Successfully purged data for %d blocks from PostgreSQL "+
				"(new height = %d):\n%v", s.Blocks, heightDB, s)
		} // otherwise likely dbtypes.ErrNoResult (heightDB was already -1)
	}

	// Get the last block added to the DB.
	lastBlockPG, err := chainDB.HeightDB()
	if err != nil {
		return fmt.Errorf("Unable to get height from PostgreSQL DB: %v", err)
	}

	// For consistency with StakeDatabase, a non-negative height is needed.
	heightDB := lastBlockPG
	if heightDB < 0 {
		heightDB = 0
	}

	charts := cache.NewChartData(ctx, uint32(heightDB), activeChain)
	chainDB.RegisterCharts(charts)

	// DB height and stakedb height must be equal. StakeDatabase will catch up
	// automatically if it is behind, but we must rewind it here if it is ahead
	// of chainDB. For chainDB to receive notification from StakeDatabase when
	// the required blocks are connected, the StakeDatabase must be at the same
	// height or lower than chainDB.
	stakeDBHeight = int64(stakeDB.Height())
	if stakeDBHeight > heightDB {
		// Have chainDB rewind it's the StakeDatabase. stakeDBHeight is
		// always rewound to a height of zero even when lastBlockPG is -1,
		// hence we rewind to heightDB.
		log.Infof("Rewinding StakeDatabase from block %d to %d.",
			stakeDBHeight, heightDB)
		stakeDBHeight, err = chainDB.RewindStakeDB(ctx, heightDB)
		if err != nil {
			return fmt.Errorf("RewindStakeDB failed: %v", err)
		}

		// Verify that the StakeDatabase is at the intended height.
		if stakeDBHeight != heightDB {
			return fmt.Errorf("failed to rewind stakedb: got %d, expecting %d",
				stakeDBHeight, heightDB)
		}
	}

	// TODO: just use getblockchaininfo to see if it still syncing and what
	// height the network's best block is at.
	blockHash, nodeHeight, err := dcrdClient.GetBestBlock(ctx)
	if err != nil {
		return fmt.Errorf("Unable to get block from node: %v", err)
	}

	block, err := dcrdClient.GetBlockHeader(ctx, blockHash)
	if err != nil {
		return fmt.Errorf("unable to fetch the block from the node: %v", err)
	}

	// bestBlockAge is the time since the dcrd best block was mined.
	bestBlockAge := time.Since(block.Timestamp).Minutes()

	// Since mining a block take approximately ChainParams.TargetTimePerBlock then the
	// expected height of the best block from dcrd now should be this.
	expectedHeight := int64(bestBlockAge/float64(activeChain.TargetTimePerBlock)) + nodeHeight

	// Estimate how far chainDB is behind the node.
	blocksBehind := expectedHeight - lastBlockPG
	if blocksBehind < 0 {
		return fmt.Errorf("Node is still syncing. Node height = %d, "+
			"DB height = %d", expectedHeight, heightDB)
	}

	// PG gets winning tickets out of baseDB's pool info cache, so it must
	// be big enough to hold the needed blocks' info, and charged with the
	// data from disk. The cache is updated on each block connect.
	tpcSize := int(blocksBehind) + 200
	log.Debugf("Setting ticket pool cache capacity to %d blocks", tpcSize)
	err = stakeDB.SetPoolCacheCapacity(tpcSize)
	if err != nil {
		return err
	}

	// Charge stakedb pool info cache, including previous PG blocks.
	if err = chainDB.ChargePoolInfoCache(heightDB - 2); err != nil {
		return fmt.Errorf("Failed to charge pool info cache: %v", err)
	}

	// Block data collector. Needs a StakeDatabase too.
	collector := blockdata.NewCollector(dcrdClient, activeChain, stakeDB)
	if collector == nil {
		return fmt.Errorf("Failed to create block data collector")
	}

	// Build a slice of each required saver type for each data source.
	blockDataSavers := []blockdata.BlockDataSaver{chainDB}
	mempoolSavers := []mempool.MempoolDataSaver{chainDB.MPC} // mempool.DataCache

	// Allow Ctrl-C to halt startup here.
	if shutdownRequested(ctx) {
		return nil
	}

	// WaitGroup for monitoring goroutines
	var wg sync.WaitGroup

	// ExchangeBot
	var xcBot *exchanges.ExchangeBot
	if cfg.EnableExchangeBot && activeChain.Name != "mainnet" {
		log.Warnf("disabling exchange monitoring. only available on mainnet")
		cfg.EnableExchangeBot = false
	}
	if cfg.EnableExchangeBot {
		botCfg := exchanges.ExchangeBotConfig{
			BtcIndex:       cfg.ExchangeCurrency,
			LTCIndex:       cfg.ExchangeCurrency,
			MasterBot:      cfg.RateMaster,
			MasterCertFile: cfg.RateCertificate,
			BinanceAPIURL:  cfg.BinanceAPI,
		}
		if cfg.DisabledExchanges != "" {
			botCfg.Disabled = strings.Split(cfg.DisabledExchanges, ",")
		}
		xcBot, err = exchanges.NewExchangeBot(&botCfg)
		if err != nil {
			log.Errorf("Could not create exchange monitor. Exchange info will be disabled: %v", err)
		} else {
			var xcList, prepend string
			tokenList := make([]string, 0)
			for k := range xcBot.Exchanges {
				if !slices.Contains(tokenList, k) {
					tokenList = append(tokenList, k)
				}
			}
			for k := range xcBot.LTCExchanges {
				if !slices.Contains(tokenList, k) {
					tokenList = append(tokenList, k)
				}
			}
			for _, k := range tokenList {
				xcList += prepend + k
				prepend = ", "
			}
			log.Infof("ExchangeBot monitoring %s", xcList)
			wg.Add(1)
			go xcBot.Start(ctx, &wg)
		}
	}

	// Creates a new or loads an existing agendas db instance that helps to
	// store and retrieves agendas data. Agendas votes are On-Chain
	// transactions that appear in the decred blockchain. If corrupted data is
	// is found, its deleted pending the data update that restores valid data.
	var agendaDB *agendas.AgendaDB
	agendaDB, err = agendas.NewAgendasDB(
		dcrdClient, filepath.Join(cfg.DataDir, cfg.AgendasDBFileName))
	if err != nil {
		return fmt.Errorf("failed to create new agendas db instance: %v", err)
	}

	// Creates a new or loads an existing proposals db instance that stores and
	// retrieves data from politeia and is used by dcrdata.
	proposalsDB, err := politeia.NewProposalsDB(cfg.PoliteiaURL,
		filepath.Join(cfg.DataDir, cfg.ProposalsFileName))
	if err != nil {
		return fmt.Errorf("failed to create new proposals db instance: %v", err)
	}

	// A vote tracker tracks current block and stake versions and votes. Only
	// initialize the vote tracker if not on simnet. nil tracker is a sentinel
	// value throughout.
	var tracker *agendas.VoteTracker
	if !cfg.SimNet {
		tracker, err = agendas.NewVoteTracker(activeChain, dcrdClient,
			chainDB.AgendaVoteCounts)
		if err != nil {
			return fmt.Errorf("Unable to initialize vote tracker: %v", err)
		}
	}
	chainDisabledMap := make(map[string]bool)
	chainDisabledMap[mutilchain.TYPEBTC] = btcDisabled
	chainDisabledMap[mutilchain.TYPELTC] = ltcDisabled
	chainDisabledMap[mutilchain.TYPEDCR] = dcrDisabled

	coinCapArr := strings.Split(cfg.CoincapActive, ",")
	coinCaps := make([]string, 0)
	if len(coinCapArr) > 0 {
		for _, coin := range coinCapArr {
			if coin == "" {
				continue
			}
			coinCaps = append(coinCaps, coin)
		}
	}

	// Create the explorer system.
	explore := explorer.New(&explorer.ExplorerConfig{
		DataSource:       chainDB,
		ChartSource:      charts,
		UseRealIP:        cfg.UseRealIP,
		AppVersion:       Version(),
		DevPrefetch:      !cfg.NoDevPrefetch,
		Viewsfolder:      "views",
		XcBot:            xcBot,
		AgendasSource:    agendaDB,
		Tracker:          tracker,
		Proposals:        proposalsDB,
		PoliteiaURL:      cfg.PoliteiaURL,
		MainnetLink:      cfg.MainnetLink,
		TestnetLink:      cfg.TestnetLink,
		ReloadHTML:       cfg.ReloadHTML,
		OnionAddress:     cfg.OnionAddress,
		ChainDisabledMap: chainDisabledMap,
		CoinCaps:         coinCaps,
	})
	// TODO: allow views config
	if explore == nil {
		return fmt.Errorf("failed to create new explorer (templates missing?)")
	}
	explore.UseSIGToReloadTemplates()
	defer explore.StopWebsocketHub()

	// Create the pub sub hub.
	psHub, err := pubsub.NewPubSubHub(chainDB)
	if err != nil {
		return fmt.Errorf("failed to create new pubsubhub: %v", err)
	}
	defer psHub.StopWebsocketHub()

	blockDataSavers = append(blockDataSavers, psHub)
	mempoolSavers = append(mempoolSavers, psHub) // individual transactions are from mempool monitor

	// Store explorerUI data after pubsubhub.
	blockDataSavers = append(blockDataSavers, explore)
	mempoolSavers = append(mempoolSavers, explore)

	// Block certain updates in explorer and pubsubhub during sync.
	explore.SetDBsSyncing(true)
	psHub.SetReady(false)

	// Create the mempool data collector.
	mpoolCollector := mempool.NewDataCollector(promiseClient, activeChain)
	if mpoolCollector == nil {
		// Shutdown goroutines.
		requestShutdown()
		return fmt.Errorf("Failed to create mempool data collector")
	}

	// The MempoolMonitor receives notifications of new transactions on
	// notify.NtfnChans.NewTxChan, and of new blocks on the same channel with a
	// nil transaction message. The mempool monitor will process the
	// transactions, and forward new ones on via the mpDataToPSHub with an
	// appropriate signal to the underlying WebSocketHub on signalToPSHub.
	signalToPSHub := psHub.HubRelay()
	signalToExplorer := explore.MempoolSignal()
	mempoolSigOuts := []chan<- pstypes.HubMessage{signalToPSHub, signalToExplorer}
	mpm, err := mempool.NewMempoolMonitor(ctx, mpoolCollector, mempoolSavers,
		activeChain, mempoolSigOuts, true)

	// Ensure the initial collect/store succeeded.
	if err != nil {
		// Shutdown goroutines.
		requestShutdown()
		return fmt.Errorf("NewMempoolMonitor: %v", err)
	}

	// Use the MempoolMonitor in DB to get unconfirmed transaction data.
	chainDB.UseMempoolChecker(mpm)

	// Prepare for sync by setting up the channels for status/progress updates
	// (barLoad) or full explorer page updates (latestBlockHash).

	// barLoad is used to send sync status updates to websocket clients (e.g.
	// browsers with the status page opened) via the goroutines launched by
	// BeginSyncStatusUpdates.
	// latestBlockHash communicates the hash of block most recently processed
	// during synchronization. This is done if all of the explorer pages (not
	// just the status page) are to be served during sync.
	var latestBlockHash chan *chainhash.Hash

	// Display the blockchain syncing status page if the number of blocks behind
	// the node's best block height are more than the set limit. The sync status
	// page should also be displayed when updateAllAddresses and newPGIndexes
	// are true, indicating maintenance or an initial sync.
	nodeHeight, chainDBHeight, err = Heights()
	if err != nil {
		return fmt.Errorf("Heights failed: %v", err)
	}
	blocksBehind = nodeHeight - chainDBHeight
	log.Debugf("dbHeight: %d / blocksBehind: %d", chainDBHeight, blocksBehind)
	displaySyncStatusPage := blocksBehind >= int64(cfg.SyncStatusLimit) || // over limit
		updateAllAddresses || newPGIndexes // maintenance or initial sync

	// Initiate the sync status monitor and the coordinating goroutines if the
	// sync status is activated, otherwise coordinate updating the full set of
	// explorer pages.
	if displaySyncStatusPage {
		// Start goroutines that keep the update the shared progress bar data,
		// and signal the websocket hub to send progress updates to clients.
		barLoad = make(chan *dbtypes.ProgressBarLoad, 2)
		explore.BeginSyncStatusUpdates(barLoad)
	} else {
		// Start a goroutine to update the explorer pages when the DB sync
		// functions send a new block hash on the following channel.
		latestBlockHash = make(chan *chainhash.Hash, 2)

		// The BlockConnected handler should not be started until after sync.
		go func() {
			// Keep receiving updates until the channel is closed, or a nil Hash
			// pointer received.
			for hash := range latestBlockHash {
				if hash == nil {
					return
				}
				// Fetch the blockdata by block hash.
				d, msgBlock, err := collector.CollectHash(hash)
				if err != nil {
					log.Warnf("failed to fetch blockdata for (%s) hash. error: %v",
						hash.String(), err)
					continue
				}

				// Store the blockdata for the explorer pages.
				if err = explore.Store(d, msgBlock); err != nil {
					log.Warnf("failed to store (%s) hash's blockdata for the explorer pages error: %v",
						hash.String(), err)
				}
			}
		}()

		// Before starting the DB sync, trigger the explorer to display data for
		// the current best block.

		// Retrieve the hash of the best block across every DB.
		latestDBBlockHash, err := dcrdClient.GetBlockHash(ctx, chainDBHeight)
		if err != nil {
			return fmt.Errorf("failed to fetch the block at height (%d): %v",
				chainDBHeight, err)
		}

		// Signal to load this block's data into the explorer. Future signals
		// will come from the sync methods of ChainDB.
		latestBlockHash <- latestDBBlockHash
	}

	// Create the Insight socket.io server, and add it to block savers if in
	// full/pg mode. Since insightSocketServer is added into the url before even
	// the sync starts, this implementation cannot be moved to
	// initiateHandlersAndCollectBlocks function.
	insightSocketServer, err := insight.NewSocketServer(activeChain, dcrdClient)
	if err != nil {
		return fmt.Errorf("Could not create Insight socket.io server: %v", err)
	}
	defer insightSocketServer.Close()
	blockDataSavers = append(blockDataSavers, insightSocketServer)

	// Start dcrdata's JSON web API.
	app := api.NewContext(&api.AppContextConfig{
		Client:            dcrdClient,
		BtcClient:         chainDB.BtcClient,
		LtcClient:         chainDB.LtcClient,
		Params:            activeChain,
		DataSource:        chainDB,
		XcBot:             xcBot,
		AgendasDBInstance: agendaDB,
		ProposalsDB:       proposalsDB,
		MaxAddrs:          cfg.MaxCSVAddrs,
		Charts:            charts,
		ChainDisabledMap:  chainDisabledMap,
		CoinCaps:          coinCaps,
	})
	getMarketCapData := func() {
		//get coin cap data from extenal api
		coinCapData := externalapi.GetCoinigyCapData(coinCaps)
		explore.CoinCapDataList = coinCapData
		app.CoinCapDataList = coinCapData
		log.Infof("Get Coincap data successfully. Blockchain list: %s", cfg.CoincapActive)
	}

	getMarketCapData()
	ticker := time.NewTicker(300 * time.Second)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				getMarketCapData()
				// do stuff
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()

	// Start the notification hander for keeping /status up-to-date.
	wg.Add(1)
	go app.StatusNtfnHandler(ctx, &wg, chainDB.UpdateChan())
	// Initial setting of DBHeight. Subsequently, Store() will send this.
	if chainDBHeight >= 0 {
		// Do not sent 4294967295 = uint32(-1) if there are no blocks.
		chainDB.SignalHeight(uint32(chainDBHeight))
	}

	// Configure the URL path to http handler router for the API.
	apiMux := api.NewAPIRouter(app, cfg.IndentJSON, cfg.UseRealIP, cfg.CompressAPI)

	// File downloads piggy-back on the API.
	fileMux := api.NewFileRouter(app, cfg.UseRealIP)

	// Configure the explorer web pages router.
	webMux := chi.NewRouter()
	if cfg.ServerHeader != "" {
		log.Debugf("Using Server HTTP response header %q", cfg.ServerHeader)
		webMux.Use(mw.Server(cfg.ServerHeader))
	}
	// Request per sec limit for "POST /verify-message" endpoint.
	reqPerSecLimit := 5.0
	// Create a rate limiter struct.
	limiter := mw.NewLimiter(reqPerSecLimit)
	limiter.SetMessage(fmt.Sprintf(
		"You have reached the maximum request limit (%g req/s)", reqPerSecLimit))

	if cfg.UseRealIP {
		webMux.Use(middleware.RealIP)
		// RealIP sets RemoteAddr
		limiter.SetIPLookups([]string{"RemoteAddr"})
	} else {
		limiter.SetIPLookups([]string{"X-Forwarded-For", "X-Real-IP", "RemoteAddr"})
	}

	webMux.Use(middleware.Recoverer)
	webMux.Use(mw.RequestBodyLimiter(1 << 21)) // 2 MiB, down from 10 MiB default
	if cfg.TrustProxy {                        // try to determine actual request scheme and host from x-forwarded-{proto,host} headers
		webMux.Use(explorer.ProxyHeaders)
	}
	if len(cfg.AllowedHosts) > 0 {
		webMux.Use(explorer.AllowedHosts(cfg.AllowedHosts))
	}

	webMux.With(explore.SyncStatusPageIntercept).Group(func(r chi.Router) {
		r.Get("/", explore.Home)
		r.Get("/visualblocks", explore.VisualBlocks)
	})
	webMux.Get("/ws", explore.RootWebsocket)
	webMux.Get("/ps", psHub.WebSocketHandler)

	// Make the static assets available under a path with the given prefix.
	mountAssetPaths := func(pathPrefix string) {
		if !strings.HasSuffix(pathPrefix, "/") {
			pathPrefix += "/"
		}

		webMux.Get(pathPrefix+"favicon.ico", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, "./public/images/favicon/favicon.ico")
		})

		cacheControlMaxAge := int64(cfg.CacheControlMaxAge)
		FileServer(webMux, pathPrefix+"js", "./public/js", cacheControlMaxAge)
		FileServer(webMux, pathPrefix+"css", "./public/css", cacheControlMaxAge)
		FileServer(webMux, pathPrefix+"fonts", "./public/fonts", cacheControlMaxAge)
		FileServer(webMux, pathPrefix+"images", "./public/images", cacheControlMaxAge)
		FileServer(webMux, pathPrefix+"dist", "./public/dist", cacheControlMaxAge)
	}
	// Mount under root (e.g. /js, /css, etc.).
	mountAssetPaths("/")

	// HTTP profiler
	if cfg.HTTPProfile {
		profPath := cfg.HTTPProfPath
		log.Warnf("Starting the HTTP profiler on path %s.", profPath)
		// http pprof uses http.DefaultServeMux
		http.Handle("/", http.RedirectHandler(profPath+"/debug/pprof/", http.StatusSeeOther))
		webMux.Mount(profPath, http.StripPrefix(profPath, http.DefaultServeMux))
	}

	// SyncStatusAPIIntercept returns a json response if the sync status page is
	// enabled (no the full explorer while syncing).
	webMux.With(explore.SyncStatusAPIIntercept).Group(func(r chi.Router) {
		// Mount the dcrdata's REST API.
		r.Mount("/api", apiMux.Mux)
		// Setup and mount the Insight API.
		insightApp := insight.NewInsightAPI(dcrdClient, chainDB,
			activeChain, mpm, cfg.IndentJSON, app.Status)
		insightApp.SetReqRateLimit(cfg.InsightReqRateLimit)
		insightMux := insight.NewInsightAPIRouter(insightApp, cfg.UseRealIP,
			cfg.CompressAPI, cfg.MaxCSVAddrs)
		r.Mount("/insight/api", insightMux.Mux)

		if insightSocketServer != nil {
			r.With(mw.NoOrigin).Get("/insight/socket.io/", insightSocketServer.ServeHTTP)
		}
	})

	// HTTP Error 503 StatusServiceUnavailable for file requests before sync.
	webMux.With(explore.SyncStatusFileIntercept).Group(func(r chi.Router) {
		r.Mount("/download", fileMux.Mux)
	})

	webMux.With(explore.SyncStatusPageIntercept).Group(func(r chi.Router) {
		r.NotFound(explore.NotFound)
		r.Mount("/explorer", explore.Mux) // legacy
		r.Route("/decred", func(rd chi.Router) {
			rd.Get("/", explore.DecredHome)
			rd.Get("/days", explore.DayBlocksListing)
			rd.Get("/weeks", explore.WeekBlocksListing)
			rd.Get("/months", explore.MonthBlocksListing)
			rd.Get("/years", explore.YearBlocksListing)
			rd.Get("/blocks", explore.Blocks)
			rd.Get("/ticketpricewindows", explore.StakeDiffWindows)
			rd.Get("/side", explore.SideChains)
			rd.Get("/rejects", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/decred/disapproved", http.StatusPermanentRedirect)
			})
			rd.Get("/disapproved", explore.DisapprovedBlocks)
			rd.Get("/mempool", explore.Mempool)
			rd.Get("/parameters", explore.ParametersPage)
			rd.With(explore.BlockHashPathOrIndexCtx).Get("/block/{blockhash}", explore.Block)
			rd.With(explorer.TransactionHashCtx).Get("/tx/{txid}", explore.TxPage)
			rd.With(explorer.TransactionHashCtx, explorer.TransactionIoIndexCtx).Get("/tx/{txid}/{inout}/{inoutid}", explore.TxPage)
			rd.With(explorer.AddressPathCtx).Get("/address/{address}", explore.AddressPage)
			rd.With(explorer.AddressPathCtx).Get("/addresstable/{address}", explore.AddressTable)
			rd.Get("/treasury", explore.TreasuryPage)
			rd.Get("/treasurytable", explore.TreasuryTable)
			rd.Get("/atomicswaps-table", explore.AtomicSwapsTable)
			rd.Get("/agendas", explore.AgendasPage)
			rd.With(explorer.AgendaPathCtx).Get("/agenda/{agendaid}", explore.AgendaPage)
			rd.Get("/proposals", explore.ProposalsPage)
			rd.With(explorer.ProposalPathCtx).Get("/proposal/{proposaltoken}", explore.ProposalPage)
			rd.Get("/decodetx", explore.DecodeTxPage)
			rd.Get("/search", explore.Search)
			rd.Get("/charts", explore.Charts)
			rd.Get("/ticketpool", explore.Ticketpool)
			rd.Get("/stats", explore.StatsPage)
			rd.Get("/market", explore.MarketPage)
			rd.Get("/marketlist", explore.CoinCapPage)
			rd.Get("/bwdash", explore.BisonWalletDashboardPage)
			rd.Get("/statistics", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/decred/stats", http.StatusPermanentRedirect)
			})
			rd.Get("/attack-cost", explore.AttackCost)
			rd.Get("/verify-message", explore.VerifyMessagePage)
			rd.Get("/stakingcalc", explore.StakeRewardCalcPage)
			rd.Get("/home-report", explore.HomeReportPage)
			rd.Get("/finance-report", explore.FinanceReportPage)
			rd.Get("/finance-report/detail", explore.FinanceDetailPage)
			rd.Get("/supply", explore.SupplyPage)
			rd.Get("/atomic-swaps", explore.AtomicSwapsPage)
		})
		mainRedirect := func(url string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query().Encode()
				if query != "" {
					query = "?" + query
				}
				http.Redirect(w, r, url+query, http.StatusPermanentRedirect)
			}
		}
		redirectOneParam := func(url string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				x := chi.URLParam(r, "x")
				if x != "" {
					x = "/" + x
				}
				query := r.URL.Query().Encode()
				if query != "" {
					query = "?" + query
				}
				http.Redirect(w, r, url+x+query, http.StatusPermanentRedirect)
			}
		}
		redirectThreeParam := func(url string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				x := chi.URLParam(r, "x")
				if x != "" {
					x = "/" + x
				}
				y := chi.URLParam(r, "y")
				if y != "" {
					y = "/" + y
				}
				z := chi.URLParam(r, "z")
				if z != "" {
					z = "/" + z
				}
				query := r.URL.Query().Encode()
				if query != "" {
					query = "?" + query
				}
				http.Redirect(w, r, url+x+y+z+query, http.StatusPermanentRedirect)
			}
		}
		r.Get("/days", mainRedirect("/decred/days"))
		r.Get("/weeks", mainRedirect("/decred/weeks"))
		r.Get("/months", mainRedirect("/decred/months"))
		r.Get("/years", mainRedirect("/decred/years"))
		r.Get("/blocks", mainRedirect("/decred/blocks"))
		r.Get("/ticketpricewindows", mainRedirect("/decred/ticketpricewindows"))
		r.Get("/side", mainRedirect("/decred/side"))
		r.Get("/rejects", mainRedirect("/decred/rejects"))
		r.Get("/disapproved", mainRedirect("/decred/disapproved"))
		r.Get("/mempool", mainRedirect("/decred/mempool"))
		r.Get("/parameters", mainRedirect("/decred/parameters"))
		r.Get("/block/{x}", redirectOneParam("/decred/block"))
		r.Get("/tx/{x}", redirectOneParam("/decred/tx"))
		r.Get("/tx/{x}/{y}/{z}", redirectThreeParam("/decred/tx"))
		r.Get("/address/{x}", redirectOneParam("/decred/address"))
		r.Get("/addresstable/{x}", redirectOneParam("/decred/addresstable"))
		r.Get("/treasury", mainRedirect("/decred/treasury"))
		r.Get("/treasurytable", mainRedirect("/decred/treasurytable"))
		r.Get("/atomicswaps-table", mainRedirect("/decred/atomicswaps-table"))
		r.Get("/agendas", mainRedirect("/decred/agendas"))
		r.Get("/agenda/{x}", redirectOneParam("/decred/agenda"))
		r.Get("/proposals", mainRedirect("/decred/proposals"))
		r.Get("/proposal/{x}", redirectOneParam("/decred/proposal"))
		r.Get("/decodetx", mainRedirect("/decred/decodetx"))
		r.Get("/search", mainRedirect("/decred/search"))
		r.Get("/charts", mainRedirect("/decred/charts"))
		r.Get("/ticketpool", mainRedirect("/decred/ticketpool"))
		r.Get("/stats", mainRedirect("/decred/stats"))
		r.Get("/market", mainRedirect("/decred/market"))
		r.Get("/marketlist", mainRedirect("/decred/marketlist"))
		r.Get("/bwdash", mainRedirect("/decred/bwdash"))
		r.Get("/statistics", mainRedirect("/decred/statistics"))
		r.Get("/attack-cost", mainRedirect("/decred/attack-cost"))
		r.Get("/verify-message", mainRedirect("/decred/verify-message"))
		r.Get("/stakingcalc", mainRedirect("/decred/stakingcalc"))
		r.Get("/home-report", mainRedirect("/decred/home-report"))
		r.Get("/finance-report", mainRedirect("/decred/finance-report"))
		r.Get("/finance-report/detail", mainRedirect("/decred/finance-report/detail"))
		r.Get("/supply", mainRedirect("/decred/supply"))
		r.Get("/atomic-swaps", mainRedirect("/decred/atomic-swaps"))
		r.Get("/about", explore.AboutPage)
		r.Get("/whatsnew", explore.WhatsNewPage)
		// MenuFormParser will typically redirect, but going to the homepage as a
		// fallback.
		r.With(explorer.MenuFormParser).Post("/set", explore.DecredHome)
		//mutilchain support
		r.Route("/chain", func(rd chi.Router) {
			rd.Get("/", explore.Home)
		})
		r.Route("/{chaintype}", func(rd chi.Router) {
			rd.Get("/", explore.MutilchainHome)
			rd.Get("/blocks", explore.MutilchainBlocks)
			rd.With(explore.MutilchainBlockHashPathOrIndexCtx).Get("/block/{blockhash}", explore.MutilchainBlockDetail)
			rd.With(explorer.TransactionHashCtx).Get("/tx/{txid}", explore.MutilchainTxPage)
			rd.Get("/mempool", explore.MutilchainMempool)
			rd.Get("/charts", explore.MutilchainCharts)
			rd.Get("/market", explore.MutilchainMarketPage)
			rd.Get("/supply", explore.SupplyPage)
			rd.Get("/visualblocks", explore.MultichainVisualBlocks)
			rd.Get("/parameters", explore.MutilchainParametersPage)
			rd.With(explorer.AddressPathCtx).Get("/address/{address}", explore.MutilchainAddressPage)
			rd.With(explorer.AddressPathCtx).Get("/addresstable/{address}", explore.MutilchainAddressTable)
		})
		r.With(mw.Tollbooth(limiter)).Post("/verify-message", explore.VerifyMessageHandler)
	})

	// Configure a page for the bare "/insight" path. This mounts the static
	// assets under /insight (e.g. /insight/js) to support the page's complete
	// loading when the root mounter is not accessible, such as the case in
	// certain reverse proxy configurations that map /insight as the root path.
	webMux.With(mw.OriginalRequestURI).Get("/insight", explore.InsightRootPage)
	// Serve static assets under /insight for when the a reverse proxy prefixes
	// all requests with "/insight". (e.g. /insight/js, /insight/css, etc.).
	mountAssetPaths("/insight")

	// Start the web server.
	listenAndServeProto(ctx, &wg, cfg.APIListen, cfg.APIProto, webMux)

	// Last chance to quit before syncing if the web server could not start.
	if shutdownRequested(ctx) {
		return nil
	}

	log.Infof("Starting blockchain sync...")

	syncChainDB := func() (int64, error) {
		// Use the plain rpcclient.Client or a rpcutils.BlockPrefetchClient.
		var bf rpcutils.BlockFetcher
		if cfg.NoBlockPrefetch {
			bf = dcrdClient
		} else {
			pfc := rpcutils.NewBlockPrefetchClient(dcrdClient)
			defer func() {
				pfc.Stop()
				log.Debugf("Block prefetcher hits = %d, misses = %d.",
					pfc.Hits(), pfc.Misses())
			}()
			bf = pfc
		}

		// Now that stakedb is either catching up or waiting for a block, start
		// the chainDB sync, which is the master block getter, retrieving and
		// making available blocks to the baseDB. In return, baseDB maintains a
		// StakeDatabase at the best block's height. For a detailed description
		// on how the DBs' synchronization is coordinated, see the documents in
		// db/dcrpg/sync.go.
		height, err := chainDB.SyncChainDB(ctx, bf, updateAllAddresses,
			newPGIndexes, latestBlockHash, barLoad)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				requestShutdown()
			}
			log.Errorf("dcrpg.SyncChainDB failed at height %d.", height)
			return height, err
		}
		app.Status.SetHeight(uint32(height))
		return height, nil
	}

	// define address summary syncing function
	syncAdressSummaryData := func() error {
		err := chainDB.SyncAddressSummary()
		if err != nil {
			log.Errorf("dcrpg.SyncAddressSummary failed")
			return err
		}
		return nil
	}
	// define coin age data syncing function
	syncCoinAgeData := func() error {
		err := chainDB.SyncCoinAgesData()
		if err != nil {
			log.Errorf("dcrpg.SyncCoinAgesData failed")
			return err
		}
		return nil
	}

	// define treasury summary syncing function
	syncTreasurySummaryData := func() error {
		err := chainDB.SyncTreasurySummary()
		if err != nil {
			log.Errorf("dcrpg.SyncTreasurySummary failed")
			return err
		}
		return nil
	}

	if !dcrDisabled {
		chainDBHeight, err = syncChainDB()
		if err != nil {
			return err
		}
		// Ensure all side chains known by dcrd are also present in the DB and
		// import them if they are not already there.
		if cfg.ImportSideChains {
			// First identify the side chain blocks that are missing from the DB.
			log.Info("Retrieving side chain blocks from dcrd...")
			sideChainBlocksToStore, nSideChainBlocks, err := chainDB.MissingSideChainBlocks()
			if err != nil {
				return fmt.Errorf("Unable to determine missing side chain blocks: %v", err)
			}
			nSideChains := len(sideChainBlocksToStore)

			// Importing side chain blocks involves only the aux (postgres) DBs
			// since stakedb only supports mainchain. TODO: Get stakedb to work with
			// side chain blocks to get ticket pool info.

			// Collect and store data for each side chain.
			log.Infof("Importing %d new block(s) from %d known side chains...",
				nSideChainBlocks, nSideChains)
			// Disable recomputing project fund balance, and clearing address
			// balance and counts cache.
			chainDB.InBatchSync = true
			var sideChainsStored, sideChainBlocksStored int
			for _, sideChain := range sideChainBlocksToStore {
				// Process this side chain only if there are blocks in it that need
				// to be stored.
				if len(sideChain.Hashes) == 0 {
					continue
				}
				sideChainsStored++

				// Collect and store data for each block in this side chain.
				for _, hash := range sideChain.Hashes {
					// Validate the block hash.
					blockHash, err := chainhash.NewHashFromStr(hash)
					if err != nil {
						log.Errorf("Invalid block hash %s: %v.", hash, err)
						continue
					}

					// Collect block data.
					_, msgBlock, err := collector.CollectHash(blockHash)
					if err != nil {
						// Do not quit if unable to collect side chain block data.
						log.Errorf("Unable to collect data for side chain block %s: %v.",
							hash, err)
						continue
					}

					// Get the chainwork
					chainWork, err := rpcutils.GetChainWork(chainDB.Client, blockHash)
					if err != nil {
						log.Errorf("GetChainWork failed (%s): %v", blockHash, err)
						continue
					}

					// Main DB
					log.Debugf("Importing block %s (height %d) into DB.",
						blockHash, msgBlock.Header.Height)

					// Stake invalidation is always handled by subsequent block, so
					// add the block as valid. These are all side chain blocks.
					isValid, isMainchain := true, false

					// Existing DB records might be for mainchain and/or valid
					// blocks, so these imported blocks should not data in rows that
					// are conflicting as per the different table constraints and
					// unique indexes.
					updateExistingRecords := false

					// Store data in the DB.
					_, _, _, err = chainDB.StoreBlock(msgBlock, isValid, isMainchain,
						updateExistingRecords, true, chainWork)
					if err != nil {
						// If data collection succeeded, but storage fails, bail out
						// to diagnose the DB trouble.
						return fmt.Errorf("ChainDB.StoreBlock failed: %w", err)
					}

					sideChainBlocksStored++
				}
			}
			chainDB.InBatchSync = false
			log.Infof("Successfully added %d blocks from %d side chains into dcrpg DB.",
				sideChainBlocksStored, sideChainsStored)
		}
		//synchronize legacy address data
		log.Infof("Starting address summary sync...")
		err = syncAdressSummaryData()
		if err != nil {
			return err
		}

		//synchronize treasury summary data
		log.Infof("Starting treasury summary sync...")
		err = syncTreasurySummaryData()
		if err != nil {
			return err
		}

		log.Infof("Finished treasury summary sync")

		//Synchronize DCR's price by month
		syncDailyMarketData := func() error {
			log.Infof("Starting DCR monthly price sync...")
			err := chainDB.SyncMonthlyPrice(ctx)
			if err != nil {
				log.Errorf("dcrpg.SyncMonthlyPrice failed")
				return err
			}
			log.Infof("Finished DCR monthly price sync")
			log.Infof("Starting DCR daily market data sync...")
			err = chainDB.SyncDailyMarket(ctx)
			if err != nil {
				log.Errorf("dcrpg.SyncDailyMarket failed")
				return err
			}
			log.Infof("Finished DCR daily market data sync")
			return nil
		}

		// synchorozie monthly_price and daily_market data everyday
		go func() {
			// Chạy lần đầu ngay lập tức
			err = syncDailyMarketData()
			if err != nil {
				log.Errorf("Sync daily market data failed: %v", err)
			}
			// Lặp lại mỗi 24h
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					err = syncDailyMarketData()
					if err != nil {
						log.Errorf("Sync daily market data failed: %v", err)
					}
				}
			}
		}()

		// check and sync for tspend votes
		// check tspend_votes table exist
		rowCount, err := chainDB.CountTSpendVotesRows()
		if err != nil {
			return fmt.Errorf("Count tspend_votes rows failed: %v", err)
		}
		if rowCount <= 0 {
			log.Infof("Begin checking and syncing tspend_votes table...")
			syncTSpendVotesData := func() error {
				err := chainDB.SyncTSpendVotesData()
				if err != nil {
					log.Errorf("dcrpg.SyncTSpendVotesData failed")
					return err
				}
				return nil
			}
			err = syncTSpendVotesData()
			if err != nil {
				return err
			}
			log.Infof("Finish checking and syncing tspend_votes table...")
		}

		// sync coin age table
		err = chainDB.CheckAndCreateCoinAgeTable()
		if err != nil {
			return fmt.Errorf("check and create coin_age table failed: %v", err)
		}
		err = chainDB.CheckAndCreateUtxoHistoryTable()
		if err != nil {
			return fmt.Errorf("check and create utxo_history table failed: %v", err)
		}
		err = chainDB.CheckAndCreateCoinAgeBandsTable()
		if err != nil {
			return fmt.Errorf("check and create coin_age_bands table failed: %v", err)
		}
		log.Infof("Begin checking and syncing coin age tables...")
		err = syncCoinAgeData()
		if err != nil {
			return err
		}
		log.Infof("Finish checking and syncing coin age tables...")
	}
	log.Debugf("Start sync btc/ltc tx count")
	go chainDB.SyncMultichainMetaInfo(btcDisabled, ltcDisabled)
	// After sync and indexing, must use upsert statement, which checks for
	// duplicate entries and updates instead of erroring. SyncChainDB should
	// set this on successful sync, but do it again anyway.
	chainDB.EnableDuplicateCheckOnInsert(true)

	// Exits immediately after the sync completes if SyncAndQuit is to true
	// because all we needed then was the blockchain sync be completed successfully.
	if cfg.SyncAndQuit {
		log.Infof("All ready, at height %d. Quitting.", chainDBHeight)
		return nil
	}

	// Pre-populate charts data using the dumped cache data in the .gob file
	// path provided instead of querying the data from the dbs. Should be
	// invoked before explore.Store to avoid double charts data cache
	// population. This charts pre-population is faster than db querying and can
	// be done before the monitors are fully set up.
	dumpPath := filepath.Join(cfg.DataDir, cfg.ChartsCacheDump)
	if err = charts.Load(dumpPath); err != nil {
		log.Warnf("Failed to load charts data cache: %v", err)
	} else {
		explore.ChartsUpdated()
	}
	// Dump the cache charts data into a file for future use on system exit.
	defer charts.Dump(dumpPath)

	// Add charts saver method after explorer and database stores. This may run
	// asynchronously.
	blockDataSavers = append(blockDataSavers, blockdata.BlockTrigger{
		Async: true,
		Saver: func(hash string, height uint32) error {
			log.Infof("Start trigger update chart data when have new block: %d", height)
			if err := charts.TriggerUpdate(hash, height); err != nil {
				return err
			}
			explore.ChartsUpdated()
			log.Infof("Finish trigger update chart data when have new block: %d", height)
			return nil
		},
	})

	// Block further usage of the barLoad by sending a nil value
	if barLoad != nil {
		select {
		case barLoad <- nil:
		default:
		}
	}

	// Set that newly sync'd blocks should no longer be stored in the explorer.
	// Monitors that fetch the latest updates from dcrd will be launched next.
	if latestBlockHash != nil {
		close(latestBlockHash)
	}
	if !dcrDisabled {
		// The proposals and agenda db updates are run after the db indexing.
		// Retrieve blockchain deployment updates and add them to the agendas db.
		if err = agendaDB.UpdateAgendas(); err != nil {
			return fmt.Errorf("updating agendas db failed: %v", err)
		}

		// Retrieve updates and newly added proposals from Politeia and store them
		// on our stormdb. This call is made asynchronously to not block execution
		// while the proposals db is syncing.
		log.Info("Syncing proposals data with Politeia...")
		go func() {
			if err := proposalsDB.ProposalsSync(); err != nil {
				log.Errorf("updating proposals db failed: %v", err)
			}
		}()

		// Synchronize proposal Meta data
		log.Info("Syncing proposals meta data with chain DB")
		go func() {
			//check exist and create proposal_meta table
			err := chainDB.CheckCreateProposalMetaTable()
			if err != nil {
				log.Errorf("Check exist and create proposal_meta table failed: %v", err)
				return
			}
			//get all proposals
			proposals, err := proposalsDB.GetAllProposals()
			if err != nil {
				log.Errorf("Get proposals failed: %v", err)
				return
			}
			tokens := make([]string, 0, len(proposals))
			for _, proposal := range proposals {
				tokens = append(tokens, proposal.Token)
			}
			//Get the tokens that need to be synchronized
			neededTokens, err := chainDB.GetNeededSyncProposalTokens(tokens)
			if err != nil {
				log.Errorf("Get sync needed proposals failed: %v", err)
				return
			}
			if len(neededTokens) > 0 {
				//get meta data from file
				proposalMetaDatas, err := proposalsDB.ProposalsApprovedMetadata(neededTokens, proposals)
				if err != nil {
					log.Errorf("Get proposal metadata failed: %v", err)
					return
				}
				//Add meta data to DB
				addErr := chainDB.AddProposalMeta(proposalMetaDatas)
				if addErr != nil {
					log.Errorf("Add proposal meta to DB failed: %v", addErr)
					return
				}
			}
		}()
	}
	// Monitors for new blocks, transactions, and reorgs should not run before
	// blockchain syncing and DB indexing completes. If started before then, the
	// DBs will not be prepared to process the notified events. For example, if
	// dcrd notifies of block 200000 while dcrdata has only reached 1000 in
	// batch synchronization, trying to process that block will be impossible as
	// the entire chain before it is not yet processed. Similarly, if we have
	// already registered for notifications with dcrd but the monitors below are
	// not started, notifications will fill up the channels, only to be
	// processed after sync. This is also incorrect since dcrd might notify of a
	// bew block 200000, but the batch sync will process that block on its own,
	// causing this to be a duplicate block by the time the monitors begin
	// pulling data out of the full channels.

	// The following configures and starts handlers that monitor for new blocks,
	// changes in the mempool, and handle chain reorg. It also initiates data
	// collection for the explorer.

	// Blockchain monitor for the collector
	// On reorg, only update web UI since the DB's own reorg handler will
	// deal with patching up the block info database.
	reorgBlockDataSavers := []blockdata.BlockDataSaver{explore}
	bdChainMonitor := blockdata.NewChainMonitor(ctx, collector, blockDataSavers,
		reorgBlockDataSavers)

	// Blockchain monitor for the stake DB
	sdbChainMonitor := stakeDB.NewChainMonitor(ctx)

	// Blockchain monitor for the main DB
	chainDBChainMonitor := chainDB.NewChainMonitor(ctx)
	if chainDBChainMonitor == nil {
		return fmt.Errorf("failed to enable dcrpg ChainMonitor")
	}

	// Notifications are sequenced by adding groups of notification handlers.
	// The groups are run sequentially, but the handlers within a group are run
	// concurrently. For example, register(A); register(B, C) will result in A
	// running alone and completing, then B and C running concurrently.
	notifier.RegisterBlockHandlerGroup(sdbChainMonitor.ConnectBlock)
	notifier.RegisterBlockHandlerGroup(bdChainMonitor.ConnectBlock)
	notifier.RegisterBlockHandlerLiteGroup(app.UpdateNodeHeight, mpm.BlockHandler)
	notifier.RegisterReorgHandlerGroup(sdbChainMonitor.ReorgHandler)
	notifier.RegisterReorgHandlerGroup(bdChainMonitor.ReorgHandler, chainDBChainMonitor.ReorgHandler)
	notifier.RegisterReorgHandlerGroup(charts.ReorgHandler) // snip charts data
	notifier.RegisterTxHandlerGroup(mpm.TxHandler, insightSocketServer.SendNewTx)

	// After this final node sync check, the monitors will handle new blocks.
	// TODO: make this not racy at all by having notifiers register first, but
	// enable operation on signal of sync complete.
	if !dcrDisabled {
		nodeHeight, chainDBHeight, err = Heights()
		if err != nil {
			return fmt.Errorf("Heights failed: %w", err)
		}
		if nodeHeight != chainDBHeight {
			log.Infof("Initial chain DB sync complete. Now catching up with network...")
			newPGIndexes, updateAllAddresses = false, false
			chainDBHeight, err = syncChainDB()
			if err != nil {
				return err
			}
			log.Infof("Begin checking and syncing coin age tables...")
			err = syncCoinAgeData()
			if err != nil {
				return err
			}
		}
	}

	// Set the current best block in the collection queue so that it can verify
	// that subsequent blocks are in the correct sequence.
	bestHash, bestHeight := chainDB.BestBlock()
	notifier.SetPreviousBlock(*bestHash, uint32(bestHeight))

	// Register for notifications from dcrd. This also sets the daemon RPC
	// client used by other functions in the notify/notification package (i.e.
	// common ancestor identification in processReorg).
	cerr := notifier.Listen(ctx, dcrdClient)
	if cerr != nil {
		return fmt.Errorf("RPC client error: %v (%v)", cerr.Error(), cerr.Cause())
	}

	// Update the treasury balance, and clear any cached address data in case
	// the sync status page not intercepting requests (see SyncStatusLimit).
	_ = chainDB.FreshenAddressCaches(true, nil) // async treasury queries, no error

	log.Infof("All ready, at height %d.", chainDBHeight)
	explore.SetDBsSyncing(false) // let explorer.Store do final updates
	psHub.SetReady(true)         // make the psHub's WebsocketHub ready to send

	// Initial data summary for web ui and pubsubhub. Normally the notification
	// handlers will do Collect followed by Store.
	{
		blockData, msgBlock, err := collector.Collect()
		if err != nil {
			return fmt.Errorf("Block data collection for initial summary failed: %w", err)
		}

		// Update the current chain state in the ChainDB.
		chainDB.UpdateChainState(blockData.BlockchainInfo)
		log.Infof("Current DCP0010 activation height is %d.", chainDB.DCP0010ActivationHeight())
		log.Infof("Current DCP0011 activation height is %d.", chainDB.DCP0011ActivationHeight())
		log.Infof("Current DCP0012 activation height is %d.", chainDB.DCP0012ActivationHeight())

		if err = explore.Store(blockData, msgBlock); err != nil {
			return fmt.Errorf("Failed to store initial block data for explorer pages: %w", err)
		}

		if err = psHub.Store(blockData, msgBlock); err != nil {
			return fmt.Errorf("Failed to store initial block data with the PubSubHub: %w", err)
		}
	}

	var btcCollector *blockdatabtc.Collector
	var ltcCollector *blockdataltc.Collector
	var ltcNewPGIndexes, ltcUpdateAllAddresses bool
	var btcNewPGIndexes, btcUpdateAllAddresses bool
	//start - init rpcclient for all blockchain
	if !ltcDisabled {
		//Check and init table of database
		checkErr := chainDB.MutilchainCheckAndCreateTable(mutilchain.TYPELTC)
		if checkErr != nil {
			return fmt.Errorf("Check and create table for blockchain %s errors: %w", mutilchain.TYPELTC, checkErr)
		}
		//first, use external socket api to get mempool info
		mainSocket, err := chainsocket.NewMutilchainInfoSocket(explore, mutilchain.TYPELTC)
		if err == nil {
			err = mainSocket.StartMempoolConnectAndUpdate()
		}
		if err != nil {
			log.Infof("Create external API socket failed. Start initialize mempool data with Mempool collector")
			if !chainDB.ChainDBDisabled {
				//handler mempool with Mempool monitor
				ltcMempoolSavers := []mempoolltc.MempoolDataSaver{chainDB.LTCMPC}
				ltcMempoolSavers = append(ltcMempoolSavers, explore)
				// Create the mempool data collector.
				ltcMpoolCollector := mempoolltc.NewDataCollector(ltcdClient, ltcActiveChain)
				if ltcMpoolCollector == nil {
					// Shutdown goroutines.
					requestShutdown()
					return fmt.Errorf("Failed to create LTC mempool data collector")
				}

				mpm, err := mempoolltc.NewMempoolMonitor(ctx, ltcMpoolCollector, ltcMempoolSavers,
					ltcActiveChain, true)

				// Ensure the initial collect/store succeeded.
				if err != nil {
					// Shutdown goroutines.
					requestShutdown()
					return fmt.Errorf("NewMempoolMonitor: %v", err)
				}

				// Use the MempoolMonitor in DB to get unconfirmed transaction data.
				chainDB.UseLTCMempoolChecker(mpm)
			}
		}

		//Start - LTC Sync handler
		ltcHeightFromDB, err := chainDB.MutilchainHeightDB(mutilchain.TYPELTC)
		if err != nil {
			if err != sql.ErrNoRows {
				return fmt.Errorf("Unable to get ltc height from PostgreSQL DB: %v", err)
			}
			ltcHeightFromDB = 0
		}
		//start handler ltc chart data
		ltcCharts := cache.NewLTCChartData(ctx, uint32(ltcHeightFromDB), ltcActiveChain, int64(ltcHeight), chainDB.ChainDBDisabled)
		chainDB.RegisterMutilchainCharts(ltcCharts)

		explore.LtcChartSource = ltcCharts
		app.LtcCharts = ltcCharts
		psHub.LtcCharts = ltcCharts
		ltcDumpPath := filepath.Join(cfg.DataDir, cfg.LTCChartsCacheDump)
		if err = ltcCharts.Load(ltcDumpPath); err != nil {
			log.Warnf("Failed to load charts data cache: %v", err)
		}
		// Dump the cache charts data into a file for future use on system exit.
		defer ltcCharts.Dump(ltcDumpPath)
		if !chainDB.ChainDBDisabled {
			//finish handler ltc chart data
			ltcBlocksBehind := int64(ltcHeight) - int64(ltcHeightFromDB)
			if ltcBlocksBehind < 0 {
				return fmt.Errorf("LTC Node is still syncing. Node height = %d, "+
					"DB height = %d", ltcHeight, ltcHeightFromDB)
			}

			// Check for missing indexes.
			ltcMissingIndexes, ltcDescs, err := chainDB.MutilchainMissingIndexes(mutilchain.TYPELTC)
			if err != nil {
				return err
			}
			// If any indexes are missing, forcibly drop any existing indexes, and
			// create them all after block sync.
			if len(ltcMissingIndexes) > 0 {
				ltcNewPGIndexes = true
				ltcUpdateAllAddresses = true
				// Warn if this is not a fresh sync.
				if chainDB.MutilchainHeight(mutilchain.TYPELTC) > 0 {
					log.Warnf("Some table indexes not found on %s!", mutilchain.TYPELTC)
					for im, mi := range ltcMissingIndexes {
						log.Warnf(`%s - Missing Index "%s": "%s"`, mutilchain.TYPELTC, mi, ltcDescs[im])
					}
					log.Warnf("%s: Forcing new index creation and addresses table spending info update.", mutilchain.TYPELTC)
				}
			}
		}

		//start init collector for ltc
		ltcCollector = blockdataltc.NewCollector(ltcdClient, ltcActiveChain)
		if ltcCollector == nil {
			return fmt.Errorf("Failed to create LTC block data collector")
		}
		var ltcLatestBlockHash = make(chan *ltcchainhash.Hash, 2)
		// The BlockConnected handler should not be started until after sync.
		go func() {
			// Keep receiving updates until the channel is closed, or a nil Hash
			// pointer received.
			for hash := range ltcLatestBlockHash {
				if hash == nil {
					return
				}
				// Fetch the blockdata by block hash.
				d, msgBlock, err := ltcCollector.CollectHash(hash)
				if err != nil {
					log.Warnf("failed to fetch blockdata for (%s) hash. error: %v",
						hash.String(), err)
					continue
				}

				// Store the blockdata for the explorer pages.
				if err = explore.LTCStore(d, msgBlock); err != nil {
					log.Warnf("failed to store (%s) hash's blockdata for the explorer pages error: %v",
						hash.String(), err)
				}
			}
		}()
		// Before starting the DB sync, trigger the explorer to display data for
		// the current best block.
		// Retrieve the hash of the best block across every DB.
		ltcDaemonLastestBlockHash, ltcBestHeight, bestErr := ltcdClient.GetBestBlock()
		if bestErr != nil {
			return fmt.Errorf("failed to fetch the block at height (%d): %v",
				ltcBestHeight, bestErr)
		}

		// Signal to load this block's data into the explorer. Future signals
		// will come from the sync methods of ChainDB.
		ltcLatestBlockHash <- ltcDaemonLastestBlockHash

		if ltcCollector != nil {
			ltcBlockData, ltcMsgBlock, err := ltcCollector.Collect()
			if err != nil {
				return fmt.Errorf("Block data collection for initial summary failed: %w", err)
			}

			// Update the current chain state in the ChainDB.
			chainDB.UpdateLTCChainState(ltcBlockData.BlockchainInfo)

			if err = explore.LTCStore(ltcBlockData, ltcMsgBlock); err != nil {
				return fmt.Errorf("Failed to store initial block data for explorer pages: %w", err)
			}
		}
		//start - handler notifier for ltc
		ltcReorgBlockDataSavers := []blockdataltc.BlockDataSaver{explore}
		ltcBlockDataSavers := []blockdataltc.BlockDataSaver{}
		ltcBlockDataSavers = append(ltcBlockDataSavers, chainDB)
		ltcBlockDataSavers = append(ltcBlockDataSavers, psHub)
		ltcBlockDataSavers = append(ltcBlockDataSavers, explore)
		// Add charts saver method after explorer and database stores. This may run
		// asynchronously.
		ltcBlockDataSavers = append(ltcBlockDataSavers, blockdataltc.BlockTrigger{
			Async: true,
			Saver: func(hash string, height uint32) error {
				return ltcCharts.TriggerUpdate(hash, height)
			},
		})
		ltcBdChainMonitor := blockdataltc.NewChainMonitor(ctx, ltcCollector, ltcBlockDataSavers,
			ltcReorgBlockDataSavers)

		ltcNotifier.RegisterBlockHandlerGroup(ltcBdChainMonitor.ConnectBlock)
		// Register for notifications from dcrd. This also sets the daemon RPC
		// client used by other functions in the notify/notification package (i.e.
		// common ancestor identification in processReorg).
		cerr := ltcNotifier.Listen(ctx, ltcdClient)
		if cerr != nil {
			return fmt.Errorf("LTC RPC client error: %v (%v)", cerr.Error(), cerr.Cause())
		}
		//end - handler notifier for ltc
		//end init collector for ltc
	}

	if !btcDisabled {
		//Check and init table of database
		checkErr := chainDB.MutilchainCheckAndCreateTable(mutilchain.TYPEBTC)
		if checkErr != nil {
			return fmt.Errorf("Check and create table for blockchain %s errors: %w", mutilchain.TYPEBTC, checkErr)
		}

		//first, use external socket api to get mempool info
		mainSocket, err := chainsocket.NewMutilchainInfoSocket(explore, mutilchain.TYPEBTC)
		if err == nil {
			err = mainSocket.StartMempoolConnectAndUpdate()
		}
		if err != nil {
			log.Infof("Create external API socket failed. Start initialize mempool data with Mempool collector")
		}

		//Start - BTC Sync handler
		btcHeightFromDB, err := chainDB.MutilchainHeightDB(mutilchain.TYPEBTC)
		if err != nil {
			if err != sql.ErrNoRows {
				return fmt.Errorf("Unable to get btc height from PostgreSQL DB: %v", err)
			}
			btcHeightFromDB = 0
		}
		//start handler btc chart data
		btcCharts := cache.NewBTCChartData(ctx, uint32(btcHeightFromDB), btcActiveChain, int64(btcHeight), chainDB.ChainDBDisabled)
		chainDB.RegisterMutilchainCharts(btcCharts)

		explore.BtcChartSource = btcCharts
		app.BtcCharts = btcCharts
		psHub.BtcCharts = btcCharts
		btcDumpPath := filepath.Join(cfg.DataDir, cfg.BTCChartsCacheDump)
		if err = btcCharts.Load(btcDumpPath); err != nil {
			log.Warnf("Failed to load charts data cache: %v", err)
		}
		// Dump the cache charts data into a file for future use on system exit.
		defer btcCharts.Dump(btcDumpPath)
		if !chainDB.ChainDBDisabled {
			//finish handler btc chart data
			btcBlocksBehind := int64(btcHeight) - int64(btcHeightFromDB)
			if btcBlocksBehind < 0 {
				return fmt.Errorf("BTC Node is still syncing. Node height = %d, "+
					"DB height = %d", btcHeight, btcHeightFromDB)
			}

			// Check for missing indexes.
			btcMissingIndexes, btcDescs, err := chainDB.MutilchainMissingIndexes(mutilchain.TYPEBTC)
			if err != nil {
				return err
			}
			// If any indexes are missing, forcibly drop any existing indexes, and
			// create them all after block sync.
			if len(btcMissingIndexes) > 0 {
				btcNewPGIndexes = true
				btcUpdateAllAddresses = true
				// Warn if this is not a fresh sync.
				if chainDB.MutilchainHeight(mutilchain.TYPEBTC) > 0 {
					log.Warnf("Some table indexes not found on %s!", mutilchain.TYPEBTC)
					for im, mi := range btcMissingIndexes {
						log.Warnf(`%s - Missing Index "%s": "%s"`, mutilchain.TYPEBTC, mi, btcDescs[im])
					}
					log.Warnf("%s: Forcing new index creation and addresses table spending info update.", mutilchain.TYPEBTC)
				}
			}
		}

		//start init collector for btc
		btcCollector = blockdatabtc.NewCollector(btcdClient, btcActiveChain)
		if btcCollector == nil {
			return fmt.Errorf("Failed to create BTC block data collector")
		}
		var btcLatestBlockHash = make(chan *btcchainhash.Hash, 2)
		// The BlockConnected handler should not be started until after sync.
		go func() {
			// Keep receiving updates until the channel is closed, or a nil Hash
			// pointer received.
			for hash := range btcLatestBlockHash {
				if hash == nil {
					return
				}
				// Fetch the blockdata by block hash.
				d, msgBlock, err := btcCollector.CollectHash(hash)
				if err != nil {
					log.Warnf("failed to fetch blockdata for (%s) hash. error: %v",
						hash.String(), err)
					continue
				}

				// Store the blockdata for the explorer pages.
				if err = explore.BTCStore(d, msgBlock); err != nil {
					log.Warnf("failed to store (%s) hash's blockdata for the explorer pages error: %v",
						hash.String(), err)
				}
			}
		}()

		// Before starting the DB sync, trigger the explorer to display data for
		// the current best block.

		// Retrieve the hash of the best block across every DB.
		btcDaemonLastestBlockHash, btcBestHeight, btcBestErr := btcdClient.GetBestBlock()
		if btcBestErr != nil {
			return fmt.Errorf("failed to fetch the block at height (%d): %v",
				btcBestHeight, btcBestErr)
		}

		// Signal to load this block's data into the explorer. Future signals
		// will come from the sync methods of ChainDB.
		btcLatestBlockHash <- btcDaemonLastestBlockHash
		if !btcDisabled && btcCollector != nil {
			btcBlockData, btcMsgBlock, err := btcCollector.Collect()
			if err != nil {
				return fmt.Errorf("Block data collection for initial summary failed: %w", err)
			}

			// Update the current chain state in the ChainDB.
			chainDB.UpdateBTCChainState(btcBlockData.BlockchainInfo)

			if err = explore.BTCStore(btcBlockData, btcMsgBlock); err != nil {
				return fmt.Errorf("Failed to store initial block data for explorer pages: %w", err)
			}
		}
		//start - handler notifier for ltc
		btcBlockDataSavers := []blockdatabtc.BlockDataSaver{}
		btcBlockDataSavers = append(btcBlockDataSavers, chainDB)
		btcBlockDataSavers = append(btcBlockDataSavers, psHub)
		btcBlockDataSavers = append(btcBlockDataSavers, explore)
		// Add charts saver method after explorer and database stores. This may run
		// asynchronously.
		btcBlockDataSavers = append(btcBlockDataSavers, blockdatabtc.BlockTrigger{
			Async: true,
			Saver: func(hash string, height uint32) error {
				return btcCharts.TriggerUpdate(hash, height)
			},
		})
		btcReorgBlockDataSavers := []blockdatabtc.BlockDataSaver{explore}
		btcBdChainMonitor := blockdatabtc.NewChainMonitor(ctx, btcCollector, btcBlockDataSavers,
			btcReorgBlockDataSavers)

		btcNotifier.RegisterBlockHandlerGroup(btcBdChainMonitor.ConnectBlock)
		// Register for notifications from dcrd. This also sets the daemon RPC
		// client used by other functions in the notify/notification package (i.e.
		// common ancestor identification in processReorg).
		cerr := btcNotifier.Listen(ctx, btcdClient)
		if cerr != nil {
			return fmt.Errorf("BTC RPC client error: %v (%v)", cerr.Error(), cerr.Cause())
		}
		//end - handler notifier- for ltc
		//end init collector for btc
	}

	// check and sync atomic swap for Decred before sync for btc, ltc
	chainDB.SyncDecredAtomicSwap()

	go func() {
		if chainDB.ChainDBDisabled && !btcDisabled {
			err = chainDB.SyncLast20BTCBlocks(btcHeight)
			if err != nil {
				log.Error(err)
			} else {
				log.Infof("Sync last 20 BTC Blocks successfully")
			}
			// handler older btc blocks height
			// check oldest block height
			minBlockHeight, err := chainDB.GetMultichainMinBlockHeight(mutilchain.TYPEBTC)
			if err != nil {
				log.Error("Get btc min block height failed: %v", err)
			} else {
				if minBlockHeight > 1 {
					log.Infof("BTC: Start sync block time with min height. Min height: %d", minBlockHeight)
					// insert block time info to blocks table
					err = chainDB.SyncBlockTimeWithMinHeight(mutilchain.TYPEBTC, minBlockHeight)
					if err != nil {
						log.Error("Sync btc block time with min height failed: %v", err)
					} else {
						log.Infof("Sync btc block time with min height successfully. Min height: %d", minBlockHeight)
					}
				}
			}
		}
		// sync atomic swap for btc
		chainDB.SyncBTCAtomicSwap()
	}()

	go func() {
		//sync last 20 blocks
		if chainDB.ChainDBDisabled && !ltcDisabled {
			err = chainDB.SyncLast20LTCBlocks(ltcHeight)
			if err != nil {
				log.Error(err)
			} else {
				log.Infof("Sync last 20 LTC Blocks successfully")
			}
			// handler older ltc blocks height
			// check oldest block height
			minBlockHeight, err := chainDB.GetMultichainMinBlockHeight(mutilchain.TYPELTC)
			if err != nil {
				log.Error("Get ltc min block height failed: %v", err)
			} else {
				if minBlockHeight > 1 {
					log.Infof("LTC: Start sync block time with min height. Min height: %d", minBlockHeight)
					// insert block time info to blocks table
					err = chainDB.SyncBlockTimeWithMinHeight(mutilchain.TYPELTC, minBlockHeight)
					if err != nil {
						log.Error("Sync ltc block time with min height failed: %v", err)
					} else {
						log.Infof("Sync ltc block time with min height successfully. Min height: %d", minBlockHeight)
					}
				}
			}
		}
		// sync atomic swap for ltc
		chainDB.SyncLTCAtomicSwap()
	}()

	//Start sync 24h metrics
	// go chainDB.Sync24BlocksAsync()
	//End sync 24h metrics

	//end - init rpcclient
	//Start mutilchain support
	//CHeck LTC enabled
	if !ltcDisabled && ltcdClient != nil && !chainDB.ChainDBDisabled {
		quit := make(chan struct{})
		// Only accept a single CTRL+C
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		// Start waiting for the interrupt signal
		go func() {
			<-c
			signal.Stop(c)
			// Close the channel so multiple goroutines can get the message
			log.Infof("CTRL+C hit.  Closing goroutines.")
			close(quit)
		}()
		var ltcPgHeight int64
		ltcPgSyncRes := make(chan dbtypes.SyncResult)
		for {
			go chainDB.SyncLTCChainDBAsync(ltcPgSyncRes, ltcdClient, quit, ltcNewPGIndexes, ltcUpdateAllAddresses)
			// Wait for the results
			pgRes := <-ltcPgSyncRes
			ltcPgHeight = pgRes.Height
			log.Infof("PostgreSQL LTC sync ended at height %d", ltcPgHeight)

			// See if there was a SIGINT (CTRL+C)
			select {
			case <-quit:
				log.Info("Quit signal received during DB sync.")
				return nil
			default:
			}
			if pgRes.Error != nil {
				log.Errorf("dcrpg.SyncMutilchainChainDBAsync LTC failed at height: %d", pgRes.Height)
				return pgRes.Error
			}
			if ltcPgHeight == int64(ltcHeight) {
				break
			}
			// Break loop to continue starting hcexplorer.
			log.Infof("Restarting LTC sync with PostgreSQL at %d.",
				ltcPgHeight)
			ltcUpdateAllAddresses, ltcNewPGIndexes = false, false
		}
		chainDB.MutilchainEnableDuplicateCheckOnInsert(true, mutilchain.TYPELTC)
		//Finished - LTC Sync handler
	}

	//Check BTC enabled
	if !btcDisabled && btcdClient != nil && !chainDB.ChainDBDisabled {
		quit := make(chan struct{})
		// Only accept a single CTRL+C
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		// Start waiting for the interrupt signal
		go func() {
			<-c
			signal.Stop(c)
			// Close the channel so multiple goroutines can get the message
			log.Infof("CTRL+C hit.  Closing goroutines.")
			close(quit)
		}()
		var btcPgHeight int64
		btcPgSyncRes := make(chan dbtypes.SyncResult)
		for {
			go chainDB.SyncBTCChainDBAsync(btcPgSyncRes, btcdClient, quit, btcNewPGIndexes, btcUpdateAllAddresses)
			// Wait for the results
			pgRes := <-btcPgSyncRes
			btcPgHeight = pgRes.Height
			log.Infof("PostgreSQL BTC sync ended at height %d", btcPgHeight)

			// See if there was a SIGINT (CTRL+C)
			select {
			case <-quit:
				log.Info("BTC: Quit signal received during DB sync.")
				return nil
			default:
			}
			if pgRes.Error != nil {
				log.Errorf("dcrpg.SyncMutilchainChainDBAsync BTC failed at height: %d", pgRes.Height)
				return pgRes.Error
			}
			if btcPgHeight >= int64(btcHeight) {
				break
			}
			// Break loop to continue starting hcexplorer.
			log.Infof("Restarting BTC sync with PostgreSQL at %d.",
				btcPgHeight)
			btcUpdateAllAddresses, btcNewPGIndexes = false, false
		}
		chainDB.MutilchainEnableDuplicateCheckOnInsert(true, mutilchain.TYPEBTC)
		//Finished - BTC Sync handler
	}
	//End mutilchain support

	wg.Wait()

	return nil
}

func connectNodeRPC(cfg *config, ntfnHandlers *rpcclient.NotificationHandlers) (*rpcclient.Client, semver.Semver, error) {
	return rpcutils.ConnectNodeRPC(cfg.DcrdServ, cfg.DcrdUser, cfg.DcrdPass,
		cfg.DcrdCert, cfg.DisableDaemonTLS, true, ntfnHandlers)
}

func connectLTCNodeRPC(cfg *config, ntfnHandlers *ltcClient.NotificationHandlers) (*ltcClient.Client, semver.Semver, error) {
	return ltcrpcutils.ConnectNodeRPC(cfg.LtcdServ, cfg.LtcdUser, cfg.LtcdPass,
		cfg.LtcdCert, cfg.DisableDaemonTLS, true, ntfnHandlers)
}

func connectBTCNodeRPC(cfg *config, ntfnHandlers *btcClient.NotificationHandlers) (*btcClient.Client, semver.Semver, error) {
	return btcrpcutils.ConnectNodeRPC(cfg.BtcdServ, cfg.BtcdUser, cfg.BtcdPass,
		cfg.BtcdCert, cfg.DisableDaemonTLS, true, ntfnHandlers)
}

func listenAndServeProto(ctx context.Context, wg *sync.WaitGroup, listen, proto string, mux http.Handler) {
	// Try to bind web server
	server := http.Server{
		Addr:         listen,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,  // slow requests should not hold connections opened
		WriteTimeout: 60 * time.Second, // hung responses must die
	}

	// Add the graceful shutdown to the waitgroup.
	wg.Add(1)
	go func() {
		// Start graceful shutdown of web server on shutdown signal.
		<-ctx.Done()

		// We received an interrupt signal, shut down.
		log.Infof("Gracefully shutting down web server...")
		ctxShut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctxShut); err != nil {
			// Error from closing listeners.
			log.Infof("HTTP server Shutdown: %v", err)
		}

		// wg.Wait can proceed.
		wg.Done()
	}()

	log.Infof("Now serving the explorer and APIs on %s://%v/", proto, listen)
	// Start the server.
	go func() {
		var err error
		if proto == "https" {
			err = server.ListenAndServeTLS("dcrdata.cert", "dcrdata.key")
		} else {
			err = server.ListenAndServe()
		}
		// If the server dies for any reason other than ErrServerClosed (from
		// graceful server.Shutdown), log the error and request dcrdata be
		// shutdown.
		if err != nil && err != http.ErrServerClosed {
			log.Errorf("Failed to start server: %v", err)
			requestShutdown()
		}
	}()

	// If the server successfully binds to a listening port, ListenAndServe*
	// will block until the server is shutdown. Wait here briefly so the startup
	// operations in main can have a chance to bail out.
	time.Sleep(250 * time.Millisecond)
}

// FileServer conveniently sets up a http.FileServer handler to serve static
// files from path on the file system. Directory listings are denied, as are URL
// paths containing "..".
func FileServer(r chi.Router, pathRoot, fsRoot string, cacheControlMaxAge int64) {
	if strings.ContainsAny(pathRoot, "{}*") {
		panic("FileServer does not permit URL parameters.")
	}

	// Define a http.HandlerFunc to serve files but not directory indexes.
	hf := func(w http.ResponseWriter, r *http.Request) {
		// Ensure the path begins with "/".
		upath := r.URL.Path
		if strings.Contains(upath, "..") {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(upath, "/") {
			upath = "/" + upath
			r.URL.Path = upath
		}
		// Strip the path prefix and clean the path.
		upath = path.Clean(strings.TrimPrefix(upath, pathRoot))

		// Deny directory listings (http.ServeFile recognizes index.html and
		// attempts to serve the directory contents instead).
		if strings.HasSuffix(upath, "/index.html") {
			http.NotFound(w, r)
			return
		}

		// Generate the full file system path and test for existence.
		fullFilePath := filepath.Join(fsRoot, upath)
		fi, err := os.Stat(fullFilePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		// Deny directory listings
		if fi.IsDir() {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}

		http.ServeFile(w, r, fullFilePath)
	}

	// For the chi.Mux, make sure a path that ends in "/" and append a "*".
	muxRoot := pathRoot
	if pathRoot != "/" && pathRoot[len(pathRoot)-1] != '/' {
		r.Get(pathRoot, http.RedirectHandler(pathRoot+"/", 301).ServeHTTP)
		muxRoot += "/"
	}
	muxRoot += "*"

	// Mount the http.HandlerFunc on the pathRoot.
	r.With(mw.CacheControl(cacheControlMaxAge)).Get(muxRoot, hf)
}
