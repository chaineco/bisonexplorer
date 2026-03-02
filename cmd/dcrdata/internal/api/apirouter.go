// Copyright (c) 2018-2021, The Decred developers
// Copyright (c) 2017, The dcrdata developers
// See LICENSE for details.

package api

import (
	"fmt"
	"net/http"
	"strings"

	m "github.com/decred/dcrdata/cmd/dcrdata/internal/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/cors"
)

type apiMux struct {
	*chi.Mux
}

type fileMux struct {
	*chi.Mux
}

// NewAPIRouter creates a new HTTP request path router/mux for the given API,
// appContext.
func NewAPIRouter(app *appContext, JSONIndent string, useRealIP, compressLarge bool) apiMux {
	// chi router
	mux := stackedMux(useRealIP)

	// Check for and validate the "indent" URL query. Each API request handler
	// may now access the configured indentation string if indent was specified
	// and parsed as a boolean, otherwise the empty string, from
	// m.GetIndentCtx(*http.Request).
	mux.Use(m.Indent(JSONIndent))

	mux.Get("/", app.root)

	mux.Get("/status", app.status)
	mux.Get("/status/happy", app.statusHappy)
	mux.Get("/supply", app.coinSupply)
	mux.Get("/supply/circulating", app.coinSupplyCirculating)

	compMiddleware := m.Next
	if compressLarge {
		log.Debug("Enabling compressed responses for large JSON payload endpoints.")
		compMiddleware = middleware.Compress(3)
	}

	// Rate limiter for address endpoints (10 req/s per IP).
	addrReqPerSecLimit := 10.0
	addrLimiter := m.NewLimiter(addrReqPerSecLimit)
	addrLimiter.SetMessage(fmt.Sprintf(
		"You have reached the maximum request limit (%g req/s)", addrReqPerSecLimit))

	mux.Route("/block", func(r chi.Router) {
		r.Get("/avg-block-time", app.getAvgBlockTime)
		r.Route("/best", func(rd chi.Router) {
			rd.Use(app.BlockIndexLatestCtx)
			rd.Get("/", app.getBlockSummary)
			rd.Get("/height", app.currentHeight)
			rd.Get("/hash", app.getBlockHash)
			rd.Route("/header", func(rt chi.Router) {
				rt.Get("/", app.getBlockHeader)
				rt.Get("/raw", app.getBlockHeaderRaw)
			})
			rd.Get("/raw", app.getBlockRaw)
			rd.Get("/size", app.getBlockSize)
			rd.Get("/subsidy", app.blockSubsidies)
			rd.With(compMiddleware).Get("/verbose", app.getBlockVerbose)
			rd.Get("/pos", app.getBlockStakeInfoExtendedByHeight)
			rd.Route("/tx", func(rt chi.Router) {
				rt.Get("/", app.getBlockTransactions)
				rt.Get("/count", app.getBlockTransactionsCount)
			})
		})

		r.Route("/hash/{blockhash}", func(rd chi.Router) {
			rd.Use(app.BlockHashPathAndIndexCtx)
			rd.Get("/", app.getBlockSummary)
			rd.Get("/height", app.getBlockHeight)
			rd.Route("/header", func(rt chi.Router) {
				rt.Get("/", app.getBlockHeader)
				rt.Get("/raw", app.getBlockHeaderRaw)
			})
			rd.Get("/raw", app.getBlockRaw)
			rd.Get("/size", app.getBlockSize)
			rd.Get("/subsidy", app.blockSubsidies)
			rd.With(compMiddleware).Get("/verbose", app.getBlockVerbose)
			rd.Get("/pos", app.getBlockStakeInfoExtendedByHash)
			rd.Route("/tx", func(rt chi.Router) {
				rt.Get("/", app.getBlockTransactions)
				rt.Get("/count", app.getBlockTransactionsCount)
			})
		})

		r.Route("/{idx}", func(rd chi.Router) {
			rd.Use(m.BlockIndexPathCtx)
			rd.Get("/", app.getBlockSummary)
			rd.Route("/header", func(rt chi.Router) {
				rt.Get("/", app.getBlockHeader)
				rt.Get("/raw", app.getBlockHeaderRaw)
			})
			rd.Get("/hash", app.getBlockHash)
			rd.Get("/raw", app.getBlockRaw)
			rd.Get("/size", app.getBlockSize)
			rd.Get("/subsidy", app.blockSubsidies)
			rd.With(compMiddleware).Get("/verbose", app.getBlockVerbose)
			rd.Get("/pos", app.getBlockStakeInfoExtendedByHeight)
			rd.Route("/tx", func(rt chi.Router) {
				rt.Get("/", app.getBlockTransactions)
				rt.Get("/count", app.getBlockTransactionsCount)
			})
		})

		r.Route("/range/{idx0}/{idx}", func(rd chi.Router) {
			rd.Use(m.BlockIndex0PathCtx, m.BlockIndexPathCtx)
			rd.Use(compMiddleware)
			rd.Get("/", app.getBlockRangeSummary)
			rd.Get("/size", app.getBlockRangeSize)
			rd.Route("/{step}", func(rs chi.Router) {
				rs.Use(m.BlockStepPathCtx)
				rs.Get("/", app.getBlockRangeSteppedSummary)
				rs.Get("/size", app.getBlockRangeSteppedSize)
			})
		})
	})

	mux.Route("/stake", func(r chi.Router) {
		r.Route("/vote", func(rd chi.Router) {
			rd.Use(app.StakeVersionLatestCtx)
			rd.Get("/info", app.getVoteInfo)
		})
		r.Route("/pool", func(rd chi.Router) {
			rd.With(app.BlockIndexLatestCtx).Get("/", app.getTicketPoolInfo)
			rd.With(app.BlockIndexLatestCtx).Get("/full", app.getTicketPool)
			rd.With(m.BlockIndexPathCtx).Get("/b/{idx}", app.getTicketPoolInfo)
			rd.With(m.BlockIndexOrHashPathCtx).Get("/b/{idxorhash}/full", app.getTicketPool)
			rd.With(m.BlockIndex0PathCtx, m.BlockIndexPathCtx).Get("/r/{idx0}/{idx}", app.getTicketPoolInfoRange)
		})
		r.Route("/diff", func(rd chi.Router) {
			rd.Get("/", app.getStakeDiffSummary)
			rd.Get("/current", app.getStakeDiffCurrent)
			rd.Get("/estimates", app.getStakeDiffEstimates)
			rd.With(m.BlockIndexPathCtx).Get("/b/{idx}", app.getStakeDiff)
			rd.With(m.BlockIndex0PathCtx, m.BlockIndexPathCtx).Get("/r/{idx0}/{idx}", app.getStakeDiffRange)
		})
		r.Get("/powerless", app.getPowerlessTickets)
	})

	mux.Route("/tx", func(r chi.Router) {
		r.Route("/", func(rt chi.Router) {
			rt.Route("/{txid}", func(rd chi.Router) {
				rd.Use(m.TransactionHashCtx)
				rd.Get("/", app.getTransaction)
				rd.Get("/trimmed", app.getDecodedTx)
				rd.Route("/out", func(ro chi.Router) {
					ro.Get("/", app.getTransactionOutputs)
					ro.With(m.TransactionIOIndexCtx).Get("/{txinoutindex}", app.getTransactionOutput)
				})
				rd.Route("/in", func(ri chi.Router) {
					ri.Get("/", app.getTransactionInputs)
					ri.With(m.TransactionIOIndexCtx).Get("/{txinoutindex}", app.getTransactionInput)
				})
				rd.Get("/vinfo", app.getTxVoteInfo)
				rd.Get("/tinfo", app.getTxTicketInfo)
			})
		})
		r.With(m.TransactionHashCtx).Get("/hex/{txid}", app.getTransactionHex)
		r.With(m.TransactionHashCtx).Get("/decoded/{txid}", app.getDecodedTx)
		r.With(m.MultichainTxHashCtx).Get("/hex/{chaintype}/{txid}", app.getMultichainTransactionHex)
		r.With(m.MultichainTxHashCtx).Get("/decoded/{chaintype}/{txid}", app.getMultichainDecodedTx)
		r.With(m.TransactionHashCtx).Get("/swaps/{txid}", app.getTxSwapsInfo)
		r.With(m.MultichainTxHashCtx).Get("/swaps/{chaintype}/{txid}", app.getMultichainTxSwapsInfo)
	})

	mux.Route("/txs", func(r chi.Router) {
		r.Use(middleware.AllowContentType("application/json"),
			m.ValidateTxnsPostCtx, m.PostTxnsCtx)
		r.Post("/", app.getTransactions)
		r.Post("/trimmed", app.getDecodedTransactions)
	})

	// DO NOT CHANGE maxExistAddrs.
	// maxExistsAddrs must be <= 64 so that the bit mask can fit into a uint64.
	const maxExistAddrs = 64

	mux.Route("/address", func(r chi.Router) {
		r.Use(m.Tollbooth(addrLimiter))
		r.Route("/{address}", func(rd chi.Router) {
			rd.With(m.AddressPathCtxN(maxExistAddrs)).Get("/exists", app.addressExists)
			rd.Group(func(re chi.Router) {
				re.Use(m.AddressPathCtxN(1))
				re.Get("/totals", app.addressTotals)
				re.Get("/", app.getAddressTransactions)
				re.With(m.ChartGroupingCtx).Get("/types/{chartgrouping}", app.getAddressTxTypesData)
				re.With(m.ChartGroupingCtx).Get("/amountflow/{chartgrouping}", app.getAddressTxAmountFlowData)
				re.With(compMiddleware).Get("/raw", app.getAddressTransactionsRaw)
				re.Route("/count/{N}", func(ri chi.Router) {
					ri.Use(m.NPathCtx)
					ri.Get("/", app.getAddressTransactions)
					ri.With(compMiddleware).Get("/raw", app.getAddressTransactionsRaw)
					ri.Route("/skip/{M}", func(rj chi.Router) {
						rj.Use(m.MPathCtx)
						rj.Get("/", app.getAddressTransactions)
						rj.With(compMiddleware).Get("/raw", app.getAddressTransactionsRaw)
					})
				})
			})
		})
		r.Get("/addressesTxs/{addresses}", app.getAddressesTxs)
	})

	// get chart data for atomic swap transactions
	mux.Route("/atomic-swaps", func(r chi.Router) {
		r.With(m.ChartGroupingCtx).Get("/amount/{chartgrouping}", app.getSwapsAmountChartData)
		r.With(m.ChartGroupingCtx).Get("/txcount/{chartgrouping}", app.getSwapsTxcountChartData)
	})

	mux.Route("/chainaddress", func(r chi.Router) {
		r.Route("/{chaintype}/{address}", func(rd chi.Router) {
			rd.Use(m.AddressPathCtxN(1))
			rd.Get("/", app.getMutilchainAddressTransactions)
		})
	})

	// Treasury
	mux.Route("/treasury", func(r chi.Router) {
		r.Get("/balance", app.getTreasuryBalance)
		r.With(m.ChartGroupingCtx).Get("/io/{chartgrouping}", app.getTreasuryIO)
		r.Route("/votechart", func(rd chi.Router) {
			rd.With(m.TSpendVotesIdCtx).Get("/{txhash}", app.getTSpendVoteChartData)
		})
	})

	// Returns agenda data like; description, name, lockedin activated and other
	// high level agenda details for all agendas.
	mux.Route("/agendas", func(r chi.Router) {
		r.Get("/", app.getAgendasData)
	})

	// Returns the charts data for the respective individual agendas.
	mux.Route("/agenda", func(r chi.Router) {
		r.With(m.AgendaIdCtx).Get("/{agendaId}", app.getAgendaData)
	})

	mux.Route("/mempool", func(r chi.Router) {
		r.Get("/", http.NotFound /*app.getMempoolOverview*/)
		// ticket purchases
		r.Route("/sstx", func(rd chi.Router) {
			rd.Get("/", app.getSSTxSummary)
			rd.Get("/fees", app.getSSTxFees)
			rd.With(m.NPathCtx).Get("/fees/{N}", app.getSSTxFees)
			rd.Get("/details", app.getSSTxDetails)
			rd.With(m.NPathCtx).Get("/details/{N}", app.getSSTxDetails)
		})
	})

	mux.Route("/chart", func(r chi.Router) {
		// Return default chart data (ticket price)
		r.Route("/market/{token}", func(rd chi.Router) {
			rd.Use(m.ExchangeTokenContext)
			rd.With(m.StickWidthContext).Get("/candlestick/{bin}", app.getCandlestickChart)
			rd.Get("/depth", app.getDepthChart)
		})
		r.Route("/submarket/{token}", func(rd chi.Router) {
			rd.Use(m.ExchangeTokenContext)
			rd.Get("/depth", app.getDepthSubMarketChart)
		})
		r.With(m.ChartTypeCtx).Get("/{charttype}", app.ChartTypeData)
	})

	mux.Route("/chainchart", func(r chi.Router) {
		r.Route("/{chaintype}/market/{token}", func(rd chi.Router) {
			rd.Use(m.ExchangeTokenContext)
			rd.With(m.StickWidthContext).Get("/candlestick/{bin}", app.getMutilchainCandlestickChart)
			rd.Get("/depth", app.getMutilchainDepthChart)
		})
		r.Route("/{chaintype}/submarket/{token}", func(rd chi.Router) {
			rd.Use(m.ExchangeTokenContext)
			rd.Get("/depth", app.getMutilchainDepthSubmarketChart)
		})
		r.Get("/exchanges", app.getExchangeData)
		r.With(m.ChartTypeCtx).Get("/{chaintype}/{charttype}", app.MutilchainChartTypeData)
	})

	mux.Route("/stakingcalc", func(r chi.Router) {
		r.Get("/getBlocksReward", app.getBlocksReward)
	})

	mux.Route("/finance-report", func(r chi.Router) {
		r.Get("/proposal", app.getProposalReport)
		r.Get("/treasury", app.getTreasuryReport)
		r.Get("/detail", app.getReportDetail)
		r.Get("/time-range", app.getReportTimeRange)
	})

	mux.Route("/ticketpool", func(r chi.Router) {
		r.Get("/", app.getTicketPoolByDate)
		r.With(m.TicketPoolCtx).Get("/bydate/{tp}", app.getTicketPoolByDate)
		r.Get("/charts", app.getTicketPoolCharts)
	})

	mux.Route("/proposal", func(r chi.Router) {
		r.With(m.ProposalTokenCtx).Get("/{token}", app.getProposalChartData)
	})

	mux.Route("/exchangerate", func(r chi.Router) {
		r.Get("/", app.getExchangeRates)
	})

	mux.Route("/exchanges", func(r chi.Router) {
		r.Get("/", app.getExchanges)
		r.Get("/codes", app.getCurrencyCodes)
	})

	mux.Route("/broadcast", func(r chi.Router) {
		r.Get("/", app.broadcastTx)
	})

	mux.Route("/xmr", func(r chi.Router) {
		r.Get("/decode-output", app.MoneroDecodeOutputs)
		r.Get("/prove-tx", app.MoneroProveTx)
		r.Get("/transactions", app.getMoneroTransactions)
		r.Route("/block", func(rd chi.Router) {
			rd.Route("/hash/{blockhash}", func(re chi.Router) {
				re.Use(m.BlockHashPathCtx)
				re.Get("/", app.getMoneroBlockSummary)
			})
			rd.Route("/{idx}", func(re chi.Router) {
				re.Use(m.BlockIndexPathCtx)
				re.Get("/", app.getMoneroBlockSummary)
			})
		})
		r.Route("/tx", func(rd chi.Router) {
			rd.Route("/{txid}", func(re chi.Router) {
				re.Use(m.TransactionHashCtx)
				re.Get("/", app.getMoneroTransactionDetail)
			})
		})
		r.Get("/mempool", app.getMoneroMempool)
		r.Get("/networkinfo", app.getMoneroNetworkInfo)
		r.Route("/rawtransaction", func(rd chi.Router) {
			rd.Route("/{txid}", func(ri chi.Router) {
				ri.Use(m.TransactionHashCtx)
				ri.Get("/", app.getMoneroTransactionRaw)
			})
		})
	})

	mux.Route("/{chaintype}", func(r chi.Router) {
		r.Use(m.ChainTypeCtx)
		r.Route("/tx", func(rt chi.Router) {
			rt.Route("/{txid}", func(rd chi.Router) {
				rd.Use(m.TransactionHashCtx)
				rd.Get("/", app.getMultichainDecodedTx)
				rd.Route("/out", func(ro chi.Router) {
					ro.Get("/", app.getMultichainTransactionOutputs)
					ro.With(m.TransactionIOIndexCtx).Get("/{txinoutindex}", app.getMultichainTransactionOutput)
				})
				rd.Route("/in", func(ri chi.Router) {
					ri.Get("/", app.getMultichainTransactionInputs)
					ri.With(m.TransactionIOIndexCtx).Get("/{txinoutindex}", app.getMultichainTransactionInput)
				})
			})
		})
		r.Route("/address", func(rt chi.Router) {
			rt.Use(m.Tollbooth(addrLimiter))
			rt.Route("/{address}", func(rd chi.Router) {
				rd.Use(m.SimpleAddressCtx)
				rd.Get("/totals", app.multichainAddressTotals)
				rd.Get("/", app.getMultichainDBAddressTransactions)
				rd.Route("/count/{N}", func(re chi.Router) {
					re.Use(m.NPathCtx)
					re.Get("/", app.getMultichainDBAddressTransactions)
					re.Route("/skip/{M}", func(ri chi.Router) {
						ri.Use(m.MPathCtx)
						ri.Get("/", app.getMultichainDBAddressTransactions)
					})
				})
			})
		})
	})

	mux.NotFound(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, r.URL.RequestURI()+" ain't no country I've ever heard of! (404)", http.StatusNotFound)
	})

	// if cfg.PrintAPIDirectory {
	// 	var buf bytes.Buffer
	// 	json.Indent(&buf, []byte(docgen.JSONRoutesDoc(mux)), "", "\t")
	// 	buf.WriteTo(os.Stdout)

	// 	fmt.Println(docgen.MarkdownRoutesDoc(mux, docgen.MarkdownOpts{
	// 		ProjectPath: "github.com/decred/dcrdata/v5",
	// 		Intro:       "dcrdata HTTP router directory",
	// 	}))
	// 	return
	// }

	// mux.HandleFunc("/directory", APIDirectory)
	// mux.With(apiDocs(mux)).HandleFunc("/directory", APIDirectory)

	var listRoutePatterns func(routes []chi.Route) []string
	listRoutePatterns = func(routes []chi.Route) []string {
		patterns := []string{}
		for _, rt := range routes {
			patterns = append(patterns, strings.Replace(rt.Pattern, "/*", "", -1))
			if rt.SubRoutes == nil {
				continue
			}
			for _, pt := range listRoutePatterns(rt.SubRoutes.Routes()) {
				patterns = append(patterns, strings.Replace(rt.Pattern+pt, "/*", "", -1))
			}
		}
		return patterns
	}

	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		routeList := listRoutePatterns(mux.Routes())
		writeJSON(w, routeList, JSONIndent)
	})

	return apiMux{mux}
}

// NewFileRouter creates a new HTTP request path router/mux for file downloads.
func NewFileRouter(app *appContext, useRealIP bool) fileMux {
	mux := stackedMux(useRealIP)

	// Rate limiter for address file download endpoints (10 req/s per IP).
	addrReqPerSecLimit := 10.0
	addrLimiter := m.NewLimiter(addrReqPerSecLimit)
	addrLimiter.SetMessage(fmt.Sprintf(
		"You have reached the maximum request limit (%g req/s)", addrReqPerSecLimit))

	mux.Route("/address", func(rd chi.Router) {
		rd.Use(m.Tollbooth(addrLimiter))
		// Allow browser cache for 3 minutes.
		rd.Use(m.CacheControl(180))
		// The carriage return option is handled on the path to facilitate more
		// effective caching in downstream delivery.
		rd.With(m.AddressPathCtxN(1)).Get("/io/{address}", app.addressIoCsvNoCR)
		rd.With(m.AddressPathCtxN(1)).Get("/io/{address}/win", app.addressIoCsvCR)
	})

	return fileMux{mux}
}

type loggerFunc func(string, ...interface{})

func (lw loggerFunc) Printf(str string, args ...interface{}) {
	lw(str, args...)
}

// Stacks some middleware common to both file and api router.
func stackedMux(useRealIP bool) *chi.Mux {
	mux := chi.NewRouter()
	if useRealIP {
		mux.Use(middleware.RealIP)
	}
	mux.Use(middleware.Logger)
	mux.Use(middleware.Recoverer)
	corsMW := cors.Default()
	corsMW.Log = loggerFunc(apiLog.Tracef)
	mux.Use(corsMW.Handler)
	mux.Use(m.RequestBodyLimiter(1 << 21)) // 2 MiB, down from 10 MiB default
	return mux
}
