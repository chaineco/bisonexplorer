// Copyright (c) 2019-2021, The Decred developers
// See LICENSE for details.

package exchanges

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"decred.org/dcrdex/client/core"
	"decred.org/dcrdex/dex"
	dexcandles "decred.org/dcrdex/dex/candles"
	"decred.org/dcrdex/dex/msgjson"

	dcrrates "github.com/decred/dcrdata/exchanges/v3/ratesproto"
)

// Tokens. Used to identify the exchange.
const (
	Coinbase     = "coinbase"
	Coindesk     = "coindesk"
	Binance      = "binance"
	Coinex       = "coinex"
	Mexc         = "mexc"
	BTCCoinex    = "btc_coinex"
	BTCBinance   = "btc_binance"
	DragonEx     = "dragonex"
	Huobi        = "huobi"
	Poloniex     = "poloniex"
	DexDotDecred = "dcrdex"
	KuCoin       = "kucoin"
	Gemini       = "gemini"
	USDTPair     = "usdt"
	BTCPair      = "btc"
	Hotcoin      = "hotcoin"
	Xt           = "xt"
	Pionex       = "pionex"
	Bitfinex     = "bitfinex"
	Kraken       = "kraken"
)

var exchangeOrders = []string{Binance, Hotcoin, Pionex, Mexc, KuCoin, Xt, Huobi, Coinex, Gemini, Bitfinex, Kraken, BTCCoinex, BTCBinance}

// A few candlestick bin sizes.
type candlestickKey string

const (
	fiveMinKey  candlestickKey = "5m"
	halfHourKey candlestickKey = "30m"
	hourKey     candlestickKey = "1h"
	fourHourKey candlestickKey = "4h"
	dayKey      candlestickKey = "1d"
	weekKey     candlestickKey = "1w"
	monthKey    candlestickKey = "1mo"
)

var candlestickDurations = map[candlestickKey]time.Duration{
	fiveMinKey:  time.Minute * 5,
	halfHourKey: time.Minute * 30,
	hourKey:     time.Hour,
	fourHourKey: time.Hour * 4,
	dayKey:      time.Hour * 24,
	weekKey:     time.Hour * 24 * 7,
	monthKey:    time.Hour * 24 * 30,
}

func (k candlestickKey) duration() time.Duration {
	d, found := candlestickDurations[k]
	if !found {
		log.Errorf("Candlestick duration parse error for key %s", string(k))
		return time.Duration(1)
	}
	return d
}

// URLs is a set of endpoints for an exchange's various datasets.
type URLs struct {
	Price        string
	SubPrice     string
	Stats        string
	Depth        string
	Candlesticks map[candlestickKey]string
	Websocket    string
}

type requests struct {
	price        *http.Request
	subprice     *http.Request
	stats        *http.Request //nolint
	depth        *http.Request
	candlesticks map[candlestickKey]*http.Request
}

func newRequests() requests {
	return requests{
		candlesticks: make(map[candlestickKey]*http.Request),
	}
}

// Prepare the URLs.
var (
	CoinbaseURLs = URLs{
		Price: "https://api.coinbase.com/v2/exchange-rates?currency=BTC",
	}
	CoindeskURLs = URLs{
		Price: "https://api.coindesk.com/v2/bpi/currentprice.json",
	}
	BinanceURLs = URLs{
		Price: "%s/api/v3/ticker/24hr?symbol=DCR%s",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "%s/api/v3/depth?symbol=DCR%s&limit=5000",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "%s/api/v3/klines?symbol=DCR%s&interval=5m",
			halfHourKey: "%s/api/v3/klines?symbol=DCR%s&interval=30m",
			hourKey:     "%s/api/v3/klines?symbol=DCR%s&interval=1h",
			fourHourKey: "%s/api/v3/klines?symbol=DCR%s&interval=4h",
			dayKey:      "%s/api/v3/klines?symbol=DCR%s&interval=1d",
			weekKey:     "%s/api/v3/klines?symbol=DCR%s&interval=1w",
			monthKey:    "%s/api/v3/klines?symbol=DCR%s&interval=1M",
		},
	}

	BinanceMutilchainURLs = URLs{
		Price: "%s/api/v3/ticker/24hr?symbol=%sUSDT",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "%s/api/v3/depth?symbol=%sUSDT&limit=5000",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "%s/api/v3/klines?symbol=%sUSDT&interval=5m",
			halfHourKey: "%s/api/v3/klines?symbol=%sUSDT&interval=30m",
			hourKey:     "%s/api/v3/klines?symbol=%sUSDT&interval=1h",
			fourHourKey: "%s/api/v3/klines?symbol=%sUSDT&interval=4h",
			dayKey:      "%s/api/v3/klines?symbol=%sUSDT&interval=1d",
			weekKey:     "%s/api/v3/klines?symbol=%sUSDT&interval=1w",
			monthKey:    "%s/api/v3/klines?symbol=%sUSDT&interval=1M",
		},
	}

	MexcURLs = URLs{
		Price: "%s/api/mexc/v3/ticker/24hr?symbol=DCR%s",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "%s/api/mexc/v3/depth?symbol=DCR%s&limit=5000",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "%s/api/mexc/v3/klines?symbol=DCR%s&interval=5m",
			halfHourKey: "%s/api/mexc/v3/klines?symbol=DCR%s&interval=30m",
			hourKey:     "%s/api/mexc/v3/klines?symbol=DCR%s&interval=60m",
			fourHourKey: "%s/api/mexc/v3/klines?symbol=DCR%s&interval=4h",
			dayKey:      "%s/api/mexc/v3/klines?symbol=DCR%s&interval=1d",
			weekKey:     "%s/api/mexc/v3/klines?symbol=DCR%s&interval=1W",
			monthKey:    "%s/api/mexc/v3/klines?symbol=DCR%s&interval=1M",
		},
	}

	MexcMutilchainURLs = URLs{
		Price: "%s/api/mexc/v3/ticker/24hr?symbol=%sUSDT",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "%s/api/mexc/v3/depth?symbol=%sUSDT&limit=5000",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "%s/api/mexc/v3/klines?symbol=%sUSDT&interval=5m",
			halfHourKey: "%s/api/mexc/v3/klines?symbol=%sUSDT&interval=30m",
			hourKey:     "%s/api/mexc/v3/klines?symbol=%sUSDT&interval=60m",
			fourHourKey: "%s/api/mexc/v3/klines?symbol=%sUSDT&interval=4h",
			dayKey:      "%s/api/mexc/v3/klines?symbol=%sUSDT&interval=1d",
			weekKey:     "%s/api/mexc/v3/klines?symbol=%sUSDT&interval=1W",
			monthKey:    "%s/api/mexc/v3/klines?symbol=%sUSDT&interval=1M",
		},
	}

	XtURLs = URLs{
		Price: "https://sapi.xt.com/v4/public/ticker?symbol=dcr_usdt",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://sapi.xt.com/v4/public/depth?symbol=dcr_usdt&limit=500",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://sapi.xt.com/v4/public/kline?symbol=dcr_usdt&interval=5m",
			halfHourKey: "https://sapi.xt.com/v4/public/kline?symbol=dcr_usdt&interval=30m",
			hourKey:     "https://sapi.xt.com/v4/public/kline?symbol=dcr_usdt&interval=1h",
			dayKey:      "https://sapi.xt.com/v4/public/kline?symbol=dcr_usdt&interval=1d",
			weekKey:     "https://sapi.xt.com/v4/public/kline?symbol=dcr_usdt&interval=1w",
			monthKey:    "https://sapi.xt.com/v4/public/kline?symbol=dcr_usdt&interval=1M",
		},
	}

	XtMultichainURLs = URLs{
		Price: "https://sapi.xt.com/v4/public/ticker?symbol=%s_usdt",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://sapi.xt.com/v4/public/depth?symbol=%s_usdt&limit=500",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://sapi.xt.com/v4/public/kline?symbol=%s_usdt&interval=5m",
			halfHourKey: "https://sapi.xt.com/v4/public/kline?symbol=%s_usdt&interval=30m",
			hourKey:     "https://sapi.xt.com/v4/public/kline?symbol=%s_usdt&interval=1h",
			dayKey:      "https://sapi.xt.com/v4/public/kline?symbol=%s_usdt&interval=1d",
			weekKey:     "https://sapi.xt.com/v4/public/kline?symbol=%s_usdt&interval=1w",
			monthKey:    "https://sapi.xt.com/v4/public/kline?symbol=%s_usdt&interval=1M",
		},
	}

	PionexURLs = URLs{
		Price: "https://api.pionex.com/api/v1/market/tickers?symbol=DCR_USDT",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://api.pionex.com/api/v1/market/depth?symbol=DCR_USDT&limit=1000",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://api.pionex.com/api/v1/market/klines?symbol=DCR_USDT&interval=5M",
			halfHourKey: "https://api.pionex.com/api/v1/market/klines?symbol=DCR_USDT&interval=30M",
			hourKey:     "https://api.pionex.com/api/v1/market/klines?symbol=DCR_USDT&interval=60M",
			dayKey:      "https://api.pionex.com/api/v1/market/klines?symbol=DCR_USDT&interval=1D",
		},
	}

	PionexMultichainURLs = URLs{
		Price: "https://api.pionex.com/api/v1/market/tickers?symbol=%s_USDT",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://api.pionex.com/api/v1/market/depth?symbol=%s_USDT&limit=1000",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://api.pionex.com/api/v1/market/klines?symbol=%s_USDT&interval=5M",
			halfHourKey: "https://api.pionex.com/api/v1/market/klines?symbol=%s_USDT&interval=30M",
			hourKey:     "https://api.pionex.com/api/v1/market/klines?symbol=%s_USDT&interval=60M",
			dayKey:      "https://api.pionex.com/api/v1/market/klines?symbol=%s_USDT&interval=1D",
		},
	}

	HotcoinMutilchainURLs = URLs{
		Price: "https://api.hotcoinfin.com/v1/market/ticker?symbol=%s_usdt",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://api.hotcoinfin.com/v1/depth?symbol=%s_usdt&step=7246060",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://api.hotcoinfin.com/v1/ticker?symbol=%s_usdt&step=300",
			halfHourKey: "https://api.hotcoinfin.com/v1/ticker?symbol=%s_usdt&step=1800",
			hourKey:     "https://api.hotcoinfin.com/v1/ticker?symbol=%s_usdt&step=3600",
			dayKey:      "https://api.hotcoinfin.com/v1/ticker?symbol=%s_usdt&step=86400",
			weekKey:     "https://api.hotcoinfin.com/v1/ticker?symbol=%s_usdt&step=604800",
			monthKey:    "https://api.hotcoinfin.com/v1/ticker?symbol=%s_usdt&step=2592000",
		},
	}

	DragonExURLs = URLs{
		Price: "https://openapi.dragonex.io/api/v1/market/real/?symbol_id=1520101",
		// DragonEx depth chart has no parameters for configuring amount of data.
		Depth: "https://openapi.dragonex.io/api/v1/market/%s/?symbol_id=1520101", // Separate buy and sell endpoints
		Candlesticks: map[candlestickKey]string{
			hourKey: "https://openapi.dragonex.io/api/v1/market/kline/?symbol_id=1520101&count=100&kline_type=5",
			dayKey:  "https://openapi.dragonex.io/api/v1/market/kline/?symbol_id=1520101&count=100&kline_type=6",
		},
	}

	HuobiMutilchainURLs = URLs{
		Price: "https://api.huobi.pro/market/detail/merged?symbol=%susdt",
		// Huobi's only depth parameter defines bin size, 'step0' seems to mean bin
		// width of zero.
		Depth: "https://api.huobi.pro/market/depth?symbol=%susdt&type=step0",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://api.huobi.pro/market/history/kline?symbol=%susdt&period=5min&size=2000",
			halfHourKey: "https://api.huobi.pro/market/history/kline?symbol=%susdt&period=30min&size=2000",
			hourKey:     "https://api.huobi.pro/market/history/kline?symbol=%susdt&period=60min&size=2000",
			dayKey:      "https://api.huobi.pro/market/history/kline?symbol=%susdt&period=1day&size=2000",
			monthKey:    "https://api.huobi.pro/market/history/kline?symbol=%susdt&period=1mon&size=2000",
		},
	}

	HuobiURLs = URLs{
		Price: "https://api.huobi.pro/market/detail/merged?symbol=dcrusdt",
		// Huobi's only depth parameter defines bin size, 'step0' seems to mean bin
		// width of zero.
		Depth: "https://api.huobi.pro/market/depth?symbol=dcrusdt&type=step0",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://api.huobi.pro/market/history/kline?symbol=dcrusdt&period=5min&size=2000",
			halfHourKey: "https://api.huobi.pro/market/history/kline?symbol=dcrusdt&period=30min&size=2000",
			hourKey:     "https://api.huobi.pro/market/history/kline?symbol=dcrusdt&period=60min&size=2000",
			dayKey:      "https://api.huobi.pro/market/history/kline?symbol=dcrusdt&period=1day&size=2000",
			monthKey:    "https://api.huobi.pro/market/history/kline?symbol=dcrusdt&period=1mon&size=2000",
		},
	}
	PoloniexURLs = URLs{
		Price: "https://poloniex.com/public?command=returnTicker",
		// Maximum value of 100 for depth parameter.
		Depth: "https://poloniex.com/public?command=returnOrderBook&currencyPair=BTC_DCR&depth=100",
		Candlesticks: map[candlestickKey]string{
			halfHourKey: "https://poloniex.com/public?command=returnChartData&currencyPair=BTC_DCR&period=1800&start=0&resolution=auto",
			dayKey:      "https://poloniex.com/public?command=returnChartData&currencyPair=BTC_DCR&period=86400&start=0&resolution=auto",
		},
		Websocket: "wss://api2.poloniex.com",
	}

	PoloniexMutilchainURLs = URLs{
		Price: "https://poloniex.com/public?command=returnTicker",
		// Maximum value of 100 for depth parameter.
		Depth: "https://poloniex.com/public?command=returnOrderBook&currencyPair=USDT_%s&depth=100",
		Candlesticks: map[candlestickKey]string{
			halfHourKey: "https://poloniex.com/public?command=returnChartData&currencyPair=USDT_%s&period=1800&start=0&resolution=auto",
			dayKey:      "https://poloniex.com/public?command=returnChartData&currencyPair=USDT_%s&period=86400&start=0&resolution=auto",
		},
		Websocket: "wss://api2.poloniex.com",
	}

	KucoinURLs = URLs{
		Price: "https://api.kucoin.com/api/v1/market/stats?symbol=DCR-USDT",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://api.kucoin.com/api/v1/market/orderbook/level2_100?symbol=DCR-USDT",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://api.kucoin.com/api/v1/market/candles?type=5min&symbol=DCR-USDT",
			halfHourKey: "https://api.kucoin.com/api/v1/market/candles?type=30min&symbol=DCR-USDT",
			hourKey:     "https://api.kucoin.com/api/v1/market/candles?type=1hour&symbol=DCR-USDT",
			fourHourKey: "https://api.kucoin.com/api/v1/market/candles?type=4hour&symbol=DCR-USDT",
			dayKey:      "https://api.kucoin.com/api/v1/market/candles?type=1day&symbol=DCR-USDT",
			weekKey:     "https://api.kucoin.com/api/v1/market/candles?type=1week&symbol=DCR-USDT",
			monthKey:    "https://api.kucoin.com/api/v1/market/candles?type=1month&symbol=DCR-USDT",
		},
	}

	KucoinMutilchainURLs = URLs{
		Price: "https://api.kucoin.com/api/v1/market/stats?symbol=%s-USDT",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://api.kucoin.com/api/v1/market/orderbook/level2_100?symbol=%s-USDT",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://api.kucoin.com/api/v1/market/candles?type=5min&symbol=%s-USDT",
			halfHourKey: "https://api.kucoin.com/api/v1/market/candles?type=30min&symbol=%s-USDT",
			hourKey:     "https://api.kucoin.com/api/v1/market/candles?type=1hour&symbol=%s-USDT",
			fourHourKey: "https://api.kucoin.com/api/v1/market/candles?type=4hour&symbol=%s-USDT",
			dayKey:      "https://api.kucoin.com/api/v1/market/candles?type=1day&symbol=%s-USDT",
			weekKey:     "https://api.kucoin.com/api/v1/market/candles?type=1week&symbol=%s-USDT",
			monthKey:    "https://api.kucoin.com/api/v1/market/candles?type=1month&symbol=%s-USDT",
		},
	}

	CoinexURLs = URLs{
		Price: "https://api.coinex.com/v2/spot/ticker?market=%s%s",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://api.coinex.com/v2/spot/depth?market=%s%s&limit=50&interval=0",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://api.coinex.com/v2/spot/kline?market=%s%s&limit=1000&period=5min",
			halfHourKey: "https://api.coinex.com/v2/spot/kline?market=%s%s&limit=1000&period=30min",
			hourKey:     "https://api.coinex.com/v2/spot/kline?market=%s%s&limit=1000&period=1hour",
			fourHourKey: "https://api.coinex.com/v2/spot/kline?market=%s%s&limit=1000&period=4hour",
			dayKey:      "https://api.coinex.com/v2/spot/kline?market=%s%s&limit=1000&period=1day",
			weekKey:     "https://api.coinex.com/v2/spot/kline?market=%s%s&limit=1000&period=1week",
		},
	}

	GeminiMutilchainURLs = URLs{
		Price:    "https://api.gemini.com/v2/ticker/%susd",
		SubPrice: "https://api.gemini.com/v1/pubticker/%susd",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://api.gemini.com/v1/book/%susd?limit_bids=5000&limit_asks=5000",
		Candlesticks: map[candlestickKey]string{
			hourKey: "https://api.gemini.com/v2/candles/%susd/1hr",
			dayKey:  "https://api.gemini.com/v2/candles/%susd/1day",
		},
	}

	BitfinexXMRURLs = URLs{
		Price: "https://api-pub.bitfinex.com/v2/ticker/tXMRUST",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://api-pub.bitfinex.com/v2/book/tXMRUST/P0?len=100",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://api-pub.bitfinex.com/v2/candles/trade:5m:tXMRUST/hist?limit=10000",
			halfHourKey: "https://api-pub.bitfinex.com/v2/candles/trade:30m:tXMRUST/hist?limit=10000",
			hourKey:     "https://api-pub.bitfinex.com/v2/candles/trade:1h:tXMRUST/hist?limit=10000",
			dayKey:      "https://api-pub.bitfinex.com/v2/candles/trade:1D:tXMRUST/hist?limit=5000",
			weekKey:     "https://api-pub.bitfinex.com/v2/candles/trade:1W:tXMRUST/hist?limit=2000",
			monthKey:    "https://api-pub.bitfinex.com/v2/candles/trade:1M:tXMRUST/hist?limit=1000",
		},
	}

	KrakenXMRURLs = URLs{
		Price: "https://api.kraken.com/0/public/Ticker?pair=XMRUSD",
		// Binance returns a maximum of 5000 depth chart points. This seems like it
		// is the entire order book at least sometimes.
		Depth: "https://api.kraken.com/0/public/Depth?pair=XMRUSD&count=500",
		Candlesticks: map[candlestickKey]string{
			fiveMinKey:  "https://api.kraken.com/0/public/OHLC?pair=XMRUSD&interval=5",
			halfHourKey: "https://api.kraken.com/0/public/OHLC?pair=XMRUSD&interval=30",
			hourKey:     "https://api.kraken.com/0/public/OHLC?pair=XMRUSD&interval=60",
			dayKey:      "https://api.kraken.com/0/public/OHLC?pair=XMRUSD&interval=1440",
			weekKey:     "https://api.kraken.com/0/public/OHLC?pair=XMRUSD&interval=10080",
		},
	}
)

// BtcIndices maps tokens to constructors for BTC-fiat exchanges.
var BtcIndices = map[string]func(*http.Client, *BotChannels, string) (Exchange, error){
	Coinbase: NewCoinbase,
	Coindesk: NewCoindesk,
}

// DcrExchanges maps tokens to constructors for DCR-BTC exchanges.
var DcrExchanges = map[string]func(*http.Client, *BotChannels, string) (Exchange, error){
	Binance:   NewBinance,
	Mexc:      NewMexc,
	Xt:        NewXt,
	Pionex:    NewPionex,
	Coinex:    NewCoinex,
	BTCCoinex: NewBTCCoinex,
	DragonEx:  NewDragonEx,
	Huobi:     NewHuobi,
	Poloniex:  NewPoloniex,
	KuCoin:    NewKucoin,
	DexDotDecred: NewDecredDEXConstructor(&DEXConfig{
		Token:    DexDotDecred,
		Host:     "dex.decred.org:7232",
		Cert:     core.CertStore[dex.Mainnet]["dex.decred.org:7232"],
		CertHost: "dex.decred.org",
	}),
}

var LTCExchanges = map[string]func(*http.Client, *BotChannels, string, string) (Exchange, error){
	Binance:  MutilchainNewBinance,
	Hotcoin:  MutilchainNewHotcoin,
	Mexc:     MutilchainNewMexc,
	DragonEx: nil,
	Huobi:    MutilchainNewHuobi,
	Poloniex: MutilchainNewPoloniex,
	KuCoin:   MutilchainNewKucoin,
	Xt:       MutilchainNewXt,
	Pionex:   MutilchainNewPionex,
	// Gemini:       MutilchainNewGemini,
	// Coinex:       MutilchainNewCoinex,
	DexDotDecred: nil,
}

var BTCExchanges = map[string]func(*http.Client, *BotChannels, string, string) (Exchange, error){
	Binance:  MutilchainNewBinance,
	Mexc:     MutilchainNewMexc,
	Hotcoin:  MutilchainNewHotcoin,
	DragonEx: nil,
	Huobi:    MutilchainNewHuobi,
	Poloniex: MutilchainNewPoloniex,
	KuCoin:   MutilchainNewKucoin,
	Xt:       MutilchainNewXt,
	Pionex:   MutilchainNewPionex,
	// Gemini:       MutilchainNewGemini,
	// Coinex:       MutilchainNewCoinex,
	DexDotDecred: nil,
}

// add: BitFinex, Kraken
var XMRExchanges = map[string]func(*http.Client, *BotChannels, string, string) (Exchange, error){
	Mexc:   MutilchainNewMexc,
	KuCoin: MutilchainNewKucoin,
	Huobi:  MutilchainNewHuobi,
	Xt:     MutilchainNewXt,
	// Binance:  MutilchainNewBinance,
	Coinex:   MutilchainNewCoinex,
	DragonEx: nil,
	Poloniex: MutilchainNewPoloniex,
	Bitfinex: MutilchainNewBitfinex,
	Kraken:   MutilchainNewKraken,
	// Gemini:       MutilchainNewGemini,
	DexDotDecred: nil,
}

// IsBtcIndex checks whether the given token is a known Bitcoin index, as
// opposed to a Decred-to-Bitcoin Exchange.
func IsBtcIndex(token string) bool {
	_, ok := BtcIndices[token]
	return ok
}

// IsDcrExchange checks whether the given token is a known Decred-BTC exchange.
func IsDcrExchange(token string, symbol string) bool {
	if symbol != DCRUSDSYMBOL && symbol != DCRBTCSYMBOL {
		return false
	}
	_, ok := DcrExchanges[token]
	return ok
}

func IsLTCExchange(token string, symbol string) bool {
	if symbol != LTCSYMBOL {
		return false
	}
	exchange, ok := LTCExchanges[token]
	if !ok {
		return ok
	}
	return exchange != nil
}

func IsBTCExchange(token string, symbol string) bool {
	if symbol != BTCSYMBOL {
		return false
	}
	exchange, ok := BTCExchanges[token]
	if !ok {
		return ok
	}
	return exchange != nil
}

func IsXMRExchange(token string, symbol string) bool {
	if symbol != XMRSYMBOL {
		return false
	}
	exchange, ok := XMRExchanges[token]
	if !ok {
		return ok
	}
	return exchange != nil
}

// Tokens is a new slice of available exchange tokens.
func Tokens() []string {
	tokens := make([]string, 0, len(BtcIndices)+len(DcrExchanges))
	var token string
	for token = range BtcIndices {
		tokens = append(tokens, token)
	}
	for token = range DcrExchanges {
		tokens = append(tokens, token)
	}
	return tokens
}

// Most exchanges bin price values on a float precision of 8 decimal points.
// eightPtKey reliably converts the float to an int64 that is unique for a price
// bin.
func eightPtKey(rate float64) int64 {
	return int64(math.Round(rate * 1e8))
}

// Set a hard limit of an hour old for order book data. This could also be
// based on some multiple of ExchangeBotConfig.requestExpiry, but should have
// some reasonable limit anyway.
const depthDataExpiration = time.Hour

// DepthPoint is a single point in a set of depth chart data.
type DepthPoint struct {
	Quantity float64 `json:"quantity"`
	Price    float64 `json:"price"`
}

// DepthData is an exchanges order book for use in a depth chart.
type DepthData struct {
	Time int64        `json:"time"`
	Bids []DepthPoint `json:"bids"`
	Asks []DepthPoint `json:"asks"`
}

// IsFresh will be true if the data is older than depthDataExpiration.
func (depth *DepthData) IsFresh() bool {
	return time.Duration(time.Now().Unix()-depth.Time)*
		time.Second < depthDataExpiration
}

// MidGap returns the mid-gap price based on the best bid and ask. If the book
// is empty, the value 1.0 is returned.
func (depth *DepthData) MidGap() float64 {
	if len(depth.Bids) == 0 {
		if len(depth.Asks) == 0 {
			return 1
		}
		return depth.Asks[0].Price
	} else if len(depth.Asks) == 0 {
		return depth.Bids[0].Price
	}
	return (depth.Bids[0].Price + depth.Asks[0].Price) / 2
}

// Candlestick is the record of price change over some bin width of time.
type Candlestick struct {
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Open   float64   `json:"open"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume"`
	Start  time.Time `json:"start"`
}

// Candlesticks is a slice of CandleStick.
type Candlesticks []Candlestick

// returns the start time of the last Candlestick, else the zero time,
func (sticks Candlesticks) time() time.Time {
	if len(sticks) > 0 {
		return sticks[len(sticks)-1].Start
	}
	return time.Time{}
}

// Checks whether the candlestick data for the given bin size is up-to-date.
func (sticks Candlesticks) needsUpdate(bin candlestickKey) bool {
	if len(sticks) == 0 {
		return true
	}
	lastStick := sticks[len(sticks)-1]
	return time.Now().After(lastStick.Start.Add(bin.duration() * 2))
}

// BaseState are the non-iterable fields of the ExchangeState, which embeds
// BaseState.
type BaseState struct {
	Symbol string  `json:"symbol"`
	Price  float64 `json:"price"`
	// BaseVolume is poorly named. This is the volume in terms of (usually) BTC,
	// not the base asset of any particular market.
	BaseVolume float64 `json:"base_volume,omitempty"`
	Volume     float64 `json:"volume,omitempty"`
	Change     float64 `json:"change,omitempty"`
	Low        float64 `json:"low"`  // low price
	High       float64 `json:"high"` // high price
	Stamp      int64   `json:"timestamp,omitempty"`
}

// ExchangeState is the simple template for a price. The only member that is
// guaranteed is a price. For Decred exchanges, the volumes will also be
// populated.
type ExchangeState struct {
	BaseState
	Depth        *DepthData                      `json:"depth,omitempty"`
	Candlesticks map[candlestickKey]Candlesticks `json:"candlesticks,omitempty"`
	Sticks       string                          `json:"sticks"`
}

/*
func (state *ExchangeState) copy() *ExchangeState {
	newState := &ExchangeState{
		Price:      state.Price,
		BaseVolume: state.BaseVolume,
		Volume:     state.Volume,
		Change:     state.Change,
		Stamp:      state.Stamp,
		Depth:      state.Depth,
	}
	if state.Candlesticks != nil {
		newState.Candlesticks = make(map[candlestickKey]Candlesticks)
		for bin, sticks := range state.Candlesticks {
			newState.Candlesticks[bin] = sticks
		}
	}
	return newState
}
*/

// Grab any candlesticks from the top that are not in the receiver. Candlesticks
// are historical data, so never need to be discarded.
func (state *ExchangeState) stealSticks(top *ExchangeState) {
	if len(top.Candlesticks) == 0 {
		return
	}
	if state.Candlesticks == nil {
		state.Candlesticks = make(map[candlestickKey]Candlesticks)
	}
	for bin := range top.Candlesticks {
		_, have := state.Candlesticks[bin]
		if !have {
			state.Candlesticks[bin] = top.Candlesticks[bin]
		}
	}
}

// Parse an ExchangeState from a protocol buffer message.
func exchangeStateFromProto(proto *dcrrates.ExchangeRateUpdate) *ExchangeState {
	state := &ExchangeState{
		BaseState: BaseState{
			Symbol:     proto.Symbol,
			Price:      proto.GetPrice(),
			BaseVolume: proto.GetBaseVolume(),
			Volume:     proto.GetVolume(),
			Change:     proto.GetChange(),
			Stamp:      proto.GetStamp(),
		},
	}

	updateDepth := proto.GetDepth()
	if updateDepth != nil {
		depth := &DepthData{
			Time: updateDepth.Time,
			Bids: make([]DepthPoint, 0, len(updateDepth.Bids)),
			Asks: make([]DepthPoint, 0, len(updateDepth.Asks)),
		}
		for _, bid := range updateDepth.Bids {
			depth.Bids = append(depth.Bids, DepthPoint{
				Quantity: bid.Quantity,
				Price:    bid.Price,
			})
		}
		for _, ask := range updateDepth.Asks {
			depth.Asks = append(depth.Asks, DepthPoint{
				Quantity: ask.Quantity,
				Price:    ask.Price,
			})
		}
		state.Depth = depth
	}

	if proto.Candlesticks != nil {
		stickMap := make(map[candlestickKey]Candlesticks)
		for _, candlesticks := range proto.Candlesticks {
			sticks := make(Candlesticks, 0, len(candlesticks.Sticks))
			for _, stick := range candlesticks.Sticks {
				sticks = append(sticks, Candlestick{
					High:   stick.High,
					Low:    stick.Low,
					Open:   stick.Open,
					Close:  stick.Close,
					Volume: stick.Volume,
					Start:  time.Unix(stick.Start, 0),
				})
			}
			stickMap[candlestickKey(candlesticks.Bin)] = sticks
		}
		state.Candlesticks = stickMap
	}
	return state
}

// HasCandlesticks checks for data in the candlesticks map.
func (state *ExchangeState) HasCandlesticks() bool {
	return len(state.Candlesticks) > 0
}

// HasDepth is true if the there is data in the depth field.
func (state *ExchangeState) HasDepth() bool {
	return state.Depth != nil
}

// StickList is a semicolon-delimited list of available binSize.
func (state *ExchangeState) StickList() string {
	sticks := make([]string, 0, len(state.Candlesticks))
	for bin := range state.Candlesticks {
		sticks = append(sticks, string(bin))
	}
	return strings.Join(sticks, ";")
}

// ExchangeUpdate packages the ExchangeState for the update channel.
type ExchangeUpdate struct {
	Token string
	State *ExchangeState
}

// Exchange is the interface that ExchangeBot understands. Most of the methods
// are implemented by CommonExchange, but Refresh is implemented in the
// individual exchange types.
type Exchange interface {
	LastUpdate() time.Time
	LastFail() time.Time
	LastTry() time.Time
	Refresh()
	IsFailed() bool
	Token() string
	Hurry(time.Duration)
	Update(*ExchangeState)
	SilentUpdate(*ExchangeState) // skip passing update to the update channel
	UpdateIndices(FiatIndices)
}

// Doer is an interface for a *http.Client to allow testing of Refresh paths.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// CommonExchange is embedded in all of the exchange types and handles some
// state tracking and token handling for ExchangeBot communications. The
// http.Request must be created individually for each exchange.
type CommonExchange struct {
	mtx          sync.RWMutex
	Symbol       string
	token        string
	URL          string
	currentState *ExchangeState
	client       Doer
	lastUpdate   time.Time
	lastFail     time.Time
	lastRequest  time.Time
	requests     requests
	channels     *BotChannels
	mainCoin     string
	wsMtx        sync.RWMutex
	ws           websocketFeed
	wsSync       struct {
		err      error
		errCount int
		init     time.Time
		update   time.Time
		fail     time.Time
	}
	// wsProcessor is only used for websockets, not SignalR. For SignalR, the
	// callback function is passed as part of the signalrConfig.
	wsProcessor WebsocketProcessor
	// Exchanges that use websockets or signalr to maintain a live orderbook can
	// use the buy and sell slices to leverage some useful methods on
	// CommonExchange.
	orderMtx sync.RWMutex
	buys     wsOrders
	asks     wsOrders
}

// LastUpdate gets a time.Time of the last successful exchange update.
func (xc *CommonExchange) LastUpdate() time.Time {
	xc.mtx.RLock()
	defer xc.mtx.RUnlock()
	return xc.lastUpdate
}

// Hurry can be used to subtract some amount of time from the lastUpdate
// and lastFail, and can be used to de-sync the exchange updates.
func (xc *CommonExchange) Hurry(d time.Duration) {
	xc.mtx.Lock()
	defer xc.mtx.Unlock()
	xc.lastRequest = xc.lastRequest.Add(-d)
}

// LastFail gets the last time.Time of a failed exchange update.
func (xc *CommonExchange) LastFail() time.Time {
	xc.mtx.RLock()
	defer xc.mtx.RUnlock()
	return xc.lastFail
}

// IsFailed will be true if xc.lastFail > xc.lastUpdate.
func (xc *CommonExchange) IsFailed() bool {
	xc.mtx.RLock()
	defer xc.mtx.RUnlock()
	return xc.lastFail.After(xc.lastUpdate)
}

// LogRequest sets the lastRequest time.Time.
func (xc *CommonExchange) LogRequest() {
	xc.mtx.Lock()
	defer xc.mtx.Unlock()
	xc.lastRequest = time.Now()
}

// LastTry is the more recent of lastFail and LastUpdate.
func (xc *CommonExchange) LastTry() time.Time {
	xc.mtx.RLock()
	defer xc.mtx.RUnlock()
	return xc.lastRequest
}

// Token is the string associated with the exchange's token.
func (xc *CommonExchange) Token() string {
	return xc.token
}

// setLastFail sets the last failure time.
func (xc *CommonExchange) setLastFail(t time.Time) {
	xc.mtx.Lock()
	defer xc.mtx.Unlock()
	xc.lastFail = t
}

// Log the error along with the token and an additional passed identifier.
func (xc *CommonExchange) fail(msg string, err error) {
	log.Errorf("%s: %s: %v", xc.token, msg, err)
	xc.setLastFail(time.Now())
}

// Update sends an updated ExchangeState to the ExchangeBot.
func (xc *CommonExchange) Update(state *ExchangeState) {
	xc.update(state, true)
}

// SilentUpdate stores the update for internal use, but does not signal an
// update to the ExchangeBot.
func (xc *CommonExchange) SilentUpdate(state *ExchangeState) {
	xc.update(state, false)
}

func (xc *CommonExchange) update(state *ExchangeState, send bool) {
	xc.mtx.Lock()
	defer xc.mtx.Unlock()
	xc.lastUpdate = time.Now()
	state.stealSticks(xc.currentState)
	xc.currentState = state
	if !send {
		return
	}
	xc.channels.exchange <- &ExchangeUpdate{
		Token: xc.token,
		State: state,
	}
}

// UpdateIndices sends a bitcoin index update to the ExchangeBot.
func (xc *CommonExchange) UpdateIndices(indices FiatIndices) {
	xc.mtx.Lock()
	defer xc.mtx.Unlock()
	xc.lastUpdate = time.Now()
	xc.channels.index <- &IndexUpdate{
		Token:   xc.token,
		Indices: indices,
	}
}

// Send the exchange request and decode the response.
func (xc *CommonExchange) fetch(request *http.Request, response interface{}) (err error) {
	resp, err := xc.client.Do(request)
	if err != nil {
		return fmt.Errorf(fmt.Sprintf("Request failed: %v", err))
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(response)
	if err != nil {
		return fmt.Errorf(fmt.Sprintf("Failed to decode json from %s: %v", request.URL.String(), err))
	}
	return
}

// A thread-safe getter for the last known ExchangeState.
func (xc *CommonExchange) state() *ExchangeState {
	xc.mtx.RLock()
	defer xc.mtx.RUnlock()
	return xc.currentState
}

// WebsocketProcessor is a callback for new websocket messages from the server.
type WebsocketProcessor func([]byte)

// Only the fields are protected for these. (websocketFeed).Write has
// concurrency control.
func (xc *CommonExchange) websocket() (websocketFeed, WebsocketProcessor) {
	xc.mtx.RLock()
	defer xc.mtx.RUnlock()
	return xc.ws, xc.wsProcessor
}

// Creates a websocket connection and starts a listen loop. Closes any existing
// connections for this exchange.
func (xc *CommonExchange) connectWebsocket(processor WebsocketProcessor, cfg *socketConfig) error {
	ws, err := newSocketConnection(cfg)
	if err != nil {
		return err
	}

	xc.wsMtx.Lock()
	// Ensure that any previous websocket is closed.
	if xc.ws != nil {
		xc.ws.Close()
	}
	xc.wsProcessor = processor
	xc.ws = ws
	xc.wsMtx.Unlock()

	xc.startWebsocket()
	return nil
}

// The listen loop for a websocket connection.
func (xc *CommonExchange) startWebsocket() {
	ws, processor := xc.websocket()
	go func() {
		for {
			message, err := ws.Read()
			if err != nil {
				xc.setWsFail(err)
				return
			}
			processor(message)
		}
	}()
}

// wsSend sends a message on a standard websocket connection. For SignalR
// connections, use xc.sr.Send directly.
func (xc *CommonExchange) wsSend(msg interface{}) error {
	ws, _ := xc.websocket()
	if ws == nil {
		// TODO: figure out why we are sending in this state
		return errors.New("no connection")
	}
	return ws.Write(msg)
}

// Checks whether the websocketFeed Done channel is closed.
func (xc *CommonExchange) wsListening() bool {
	xc.wsMtx.RLock()
	defer xc.wsMtx.RUnlock()
	return xc.wsSync.init.After(xc.wsSync.fail)
}

// Log the error and time, and increment the error counter.
func (xc *CommonExchange) setWsFail(err error) {
	log.Errorf("%s websocket error: %v", xc.token, err)
	xc.wsMtx.Lock()
	defer xc.wsMtx.Unlock()
	if xc.ws != nil {
		xc.ws.Close()
		// Clear the field to prevent double Close'ing.
		xc.ws = nil
	}
	xc.wsSync.err = err
	xc.wsSync.errCount++
	xc.wsSync.fail = time.Now()
}

func (xc *CommonExchange) wsFailTime() time.Time {
	xc.wsMtx.RLock()
	defer xc.wsMtx.RUnlock()
	return xc.wsSync.fail
}

// Set the init flag. The websocket is considered failed if the failed flag
// is later than the init flag.
func (xc *CommonExchange) wsInitialized() {
	xc.wsMtx.Lock()
	defer xc.wsMtx.Unlock()
	xc.wsSync.init = time.Now()
	xc.wsSync.update = xc.wsSync.init
}

// Set the updated flag. Set the error count to 0 when the client has
// successfully updated.
func (xc *CommonExchange) wsUpdated() {
	xc.wsMtx.Lock()
	defer xc.wsMtx.Unlock()
	xc.wsSync.update = time.Now()
	xc.wsSync.errCount = 0
}

func (xc *CommonExchange) wsLastUpdate() time.Time {
	xc.wsMtx.RLock()
	defer xc.wsMtx.RUnlock()
	return xc.wsSync.update
}

// Checks whether the websocket is in a failed state.
func (xc *CommonExchange) wsFailed() bool {
	xc.wsMtx.RLock()
	defer xc.wsMtx.RUnlock()
	return xc.wsSync.fail.After(xc.wsSync.init)
}

// The count of errors logged since the last success-triggered reset.
func (xc *CommonExchange) wsErrorCount() int {
	xc.wsMtx.RLock()
	defer xc.wsMtx.RUnlock()
	return xc.wsSync.errCount
}

// An intermediate order representation used to track an orderbook over a
// websocket connection.
type wsOrder struct {
	price  float64
	volume float64
}
type wsOrders map[int64]*wsOrder

// Get the *wsOrder at the specified rateKey. Adds one first, if necessary.
func (ords wsOrders) order(rateKey int64, rate float64) *wsOrder {
	ord, ok := ords[rateKey]
	if ok {
		return ord
	}
	ord = &wsOrder{price: rate}
	ords[rateKey] = ord
	return ord
}

// Pull out the int64 bin keys from the map.
func wsOrderBinKeys(book wsOrders) []int64 {
	keys := make([]int64, 0, len(book))
	for k := range book {
		keys = append(keys, k)
	}
	return keys
}

// Convert the intermediate websocket orderbook to a DepthData. This function
// should be called under at least an orderMtx.RLock.
func (xc *CommonExchange) wsDepthSnapshot() *DepthData {
	askKeys := wsOrderBinKeys(xc.asks)
	sort.Slice(askKeys, func(i, j int) bool {
		return askKeys[i] < askKeys[j]
	})
	buyKeys := wsOrderBinKeys(xc.buys)
	sort.Slice(buyKeys, func(i, j int) bool {
		return buyKeys[i] > buyKeys[j]
	})
	a := make([]DepthPoint, 0, len(askKeys))
	for _, bin := range askKeys {
		pt := xc.asks[bin]
		a = append(a, DepthPoint{
			Quantity: pt.volume,
			Price:    pt.price,
		})
	}
	b := make([]DepthPoint, 0, len(buyKeys))
	for _, bin := range buyKeys {
		pt := xc.buys[bin]
		b = append(b, DepthPoint{
			Quantity: pt.volume,
			Price:    pt.price,
		})
	}
	return &DepthData{
		Time: time.Now().Unix(),
		Asks: a,
		Bids: b,
	}
}

// Grab a wsDepthSnapshot under RLock.
func (xc *CommonExchange) wsDepths() *DepthData {
	xc.orderMtx.RLock()
	defer xc.orderMtx.RUnlock()
	return xc.wsDepthSnapshot()
}

// For exchanges that have a websocket-synced orderbook, wsDepthStatus will
// return the DepthData. tryHttp will be true if the websocket is in a
// questionable state. The value of initializing will be true if this is the
// initial connection.
func (xc *CommonExchange) wsDepthStatus(connector func()) (tryHttp, initializing bool, depth *DepthData) {
	if xc.wsListening() {
		depth = xc.wsDepths()
		return
	}
	if !xc.wsFailed() {
		// Connection has not been initialized. Trigger a silent update, since an
		// update will be triggered on initial websocket message, which contains
		// the full orderbook.
		initializing = true
		log.Tracef("Initializing websocket connection for %s", xc.token)
		connector()
		return
	}
	log.Tracef("using http fallback for %s orderbook data", xc.token)
	tryHttp = true
	errCount := xc.wsErrorCount()
	var delay time.Duration
	// wsDepthStatus is only called every DataExpiry, so a delay of zero is ok
	// until there are a few consecutive errors.
	switch {
	case errCount < 5:
	case errCount < 20:
		delay = 10 * time.Minute
	default:
		delay = time.Minute * 60
	}
	okToTry := xc.wsFailTime().Add(delay)
	if time.Now().After(okToTry) {
		// Try to connect, but don't wait for the response. Grab the order
		// book over HTTP anyway.
		connector()
	} else {
		log.Errorf("%s websocket disabled. Too many errors. Will attempt to reconnect after %.1f minutes", xc.token, time.Until(okToTry).Minutes())
	}
	return

}

// Used to initialize the embedding exchanges.
func newCommonExchange(token string, client *http.Client,
	reqs requests, channels *BotChannels) *CommonExchange {
	var tZero time.Time
	return &CommonExchange{
		token:        token,
		client:       client,
		channels:     channels,
		currentState: new(ExchangeState),
		lastUpdate:   tZero,
		lastFail:     tZero,
		lastRequest:  tZero,
		requests:     reqs,
		asks:         make(wsOrders),
		buys:         make(wsOrders),
	}
}

// CoinbaseExchange provides tons of bitcoin-fiat exchange pairs.
type CoinbaseExchange struct {
	*CommonExchange
}

// NewCoinbase constructs a CoinbaseExchange.
func NewCoinbase(client *http.Client, channels *BotChannels, _ string) (coinbase Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, CoinbaseURLs.Price, nil)
	if err != nil {
		return
	}
	coinbase = &CoinbaseExchange{
		CommonExchange: newCommonExchange(Coinbase, client, reqs, channels),
	}
	return
}

// CoinbaseResponse models the JSON data returned from the Coinbase API.
type CoinbaseResponse struct {
	Data CoinbaseResponseData `json:"data"`
}

// CoinbaseResponseData models the "data" field of the Coinbase API response.
type CoinbaseResponseData struct {
	Currency string            `json:"currency"`
	Rates    map[string]string `json:"rates"`
}

// Refresh retrieves and parses API data from Coinbase.
func (coinbase *CoinbaseExchange) Refresh() {
	coinbase.LogRequest()
	response := new(CoinbaseResponse)
	err := coinbase.fetch(coinbase.requests.price, response)
	if err != nil {
		coinbase.fail("Fetch", err)
		return
	}

	indices := make(FiatIndices)
	for code, floatStr := range response.Data.Rates {
		price, err := strconv.ParseFloat(floatStr, 64)
		if err != nil {
			coinbase.fail(fmt.Sprintf("Failed to parse float for index %s. Given %s", code, floatStr), err)
			continue
		}
		indices[code] = price
	}
	coinbase.UpdateIndices(indices)
}

// CoindeskExchange provides Bitcoin indices for USD, GBP, and EUR by default.
// Others are available, but custom requests would need to be implemented.
type CoindeskExchange struct {
	*CommonExchange
}

// NewCoindesk constructs a CoindeskExchange.
func NewCoindesk(client *http.Client, channels *BotChannels, _ string) (coindesk Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, CoindeskURLs.Price, nil)
	if err != nil {
		return
	}
	coindesk = &CoindeskExchange{
		CommonExchange: newCommonExchange(Coindesk, client, reqs, channels),
	}
	return
}

// CoindeskResponse models the JSON data returned from the Coindesk API.
type CoindeskResponse struct {
	Time       CoindeskResponseTime           `json:"time"`
	Disclaimer string                         `json:"disclaimer"`
	ChartName  string                         `json:"chartName"`
	Bpi        map[string]CoindeskResponseBpi `json:"bpi"`
}

// CoindeskResponseTime models the "time" field of the Coindesk API response.
type CoindeskResponseTime struct {
	Updated    string    `json:"updated"`
	UpdatedIso time.Time `json:"updatedISO"`
	Updateduk  string    `json:"updateduk"`
}

// CoindeskResponseBpi models the "bpi" field of the Coindesk API response.
type CoindeskResponseBpi struct {
	Code        string  `json:"code"`
	Symbol      string  `json:"symbol"`
	Rate        string  `json:"rate"`
	Description string  `json:"description"`
	RateFloat   float64 `json:"rate_float"`
}

// Refresh retrieves and parses API data from Coindesk.
func (coindesk *CoindeskExchange) Refresh() {
	coindesk.LogRequest()
	response := new(CoindeskResponse)
	err := coindesk.fetch(coindesk.requests.price, response)
	if err != nil {
		coindesk.fail("Fetch", err)
		return
	}

	indices := make(FiatIndices)
	for code, bpi := range response.Bpi {
		indices[code] = bpi.RateFloat
	}
	coindesk.UpdateIndices(indices)
}

// BinanceExchange is a high-volume and well-respected crypto exchange.
type BinanceExchange struct {
	*CommonExchange
}

type HotcoinExchange struct {
	*CommonExchange
}

type MexcExchange struct {
	*CommonExchange
}

type BitfinexExchange struct {
	*CommonExchange
}

type KrakenExchange struct {
	*CommonExchange
}

// CoinexExchange is a high-volume and well-respected crypto exchange.
type CoinexExchange struct {
	*CommonExchange
}

type KucoinExchange struct {
	*CommonExchange
}

type GeminiExchange struct {
	*CommonExchange
}

type XtExchange struct {
	*CommonExchange
}

type PionexExchange struct {
	*CommonExchange
}

func MutilchainNewBinance(client *http.Client, channels *BotChannels, chainType string, binanceApiUrl string) (binance Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(BinanceMutilchainURLs.Price, binanceApiUrl, strings.ToUpper(chainType)), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(BinanceMutilchainURLs.Depth, binanceApiUrl, strings.ToUpper(chainType)), nil)
	if err != nil {
		return
	}

	for dur, url := range BinanceMutilchainURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, binanceApiUrl, strings.ToUpper(chainType)), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Binance, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)
	binance = &BinanceExchange{
		CommonExchange: commonExchange,
	}
	return
}

func MutilchainNewXt(client *http.Client, channels *BotChannels, chainType string, _ string) (xt Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(XtMultichainURLs.Price, chainType), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(XtMultichainURLs.Depth, chainType), nil)
	if err != nil {
		return
	}

	for dur, url := range XtMultichainURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, chainType), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Xt, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)
	commonExchange.mainCoin = chainType
	xt = &XtExchange{
		CommonExchange: commonExchange,
	}
	return
}

func MutilchainNewPionex(client *http.Client, channels *BotChannels, chainType string, _ string) (pionex Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(PionexMultichainURLs.Price, strings.ToUpper(chainType)), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(PionexMultichainURLs.Depth, strings.ToUpper(chainType)), nil)
	if err != nil {
		return
	}

	for dur, url := range PionexMultichainURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, strings.ToUpper(chainType)), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Pionex, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)
	commonExchange.mainCoin = chainType
	pionex = &PionexExchange{
		CommonExchange: commonExchange,
	}
	return
}

func MutilchainNewHotcoin(client *http.Client, channels *BotChannels, chainType string, _ string) (hotcoin Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(HotcoinMutilchainURLs.Price, chainType), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(HotcoinMutilchainURLs.Depth, chainType), nil)
	if err != nil {
		return
	}

	for dur, url := range HotcoinMutilchainURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, chainType), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Hotcoin, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)
	commonExchange.mainCoin = chainType
	hotcoin = &HotcoinExchange{
		CommonExchange: commonExchange,
	}
	return
}

func MutilchainNewBitfinex(client *http.Client, channels *BotChannels, chainType string, apiUrl string) (bitfinex Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, BitfinexXMRURLs.Price, nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, BitfinexXMRURLs.Depth, nil)
	if err != nil {
		return
	}

	for dur, url := range BitfinexXMRURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Bitfinex, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)
	bitfinex = &BitfinexExchange{
		CommonExchange: commonExchange,
	}
	return
}

func MutilchainNewKraken(client *http.Client, channels *BotChannels, chainType string, apiUrl string) (kraken Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, KrakenXMRURLs.Price, nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, KrakenXMRURLs.Depth, nil)
	if err != nil {
		return
	}

	for dur, url := range KrakenXMRURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Kraken, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)
	kraken = &KrakenExchange{
		CommonExchange: commonExchange,
	}
	return
}

func MutilchainNewMexc(client *http.Client, channels *BotChannels, chainType string, apiUrl string) (mexc Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(MexcMutilchainURLs.Price, apiUrl, strings.ToUpper(chainType)), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(MexcMutilchainURLs.Depth, apiUrl, strings.ToUpper(chainType)), nil)
	if err != nil {
		return
	}

	for dur, url := range MexcMutilchainURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, apiUrl, strings.ToUpper(chainType)), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Mexc, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)
	mexc = &MexcExchange{
		CommonExchange: commonExchange,
	}
	return
}

// NewBinance constructs a BinanceExchange.
func NewCoinex(client *http.Client, channels *BotChannels, _ string) (coinex Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(CoinexURLs.Price, "DCR", "USDT"), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(CoinexURLs.Depth, "DCR", "USDT"), nil)
	if err != nil {
		return
	}

	for dur, url := range CoinexURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, "DCR", "USDT"), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Coinex, client, reqs, channels)
	commonExchange.Symbol = DCRUSDSYMBOL
	coinex = &CoinexExchange{
		CommonExchange: commonExchange,
	}
	return
}

func MutilchainNewCoinex(client *http.Client, channels *BotChannels, chainType string, _ string) (coinex Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(CoinexURLs.Price, strings.ToUpper(chainType), "USDT"), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(CoinexURLs.Depth, strings.ToUpper(chainType), "USDT"), nil)
	if err != nil {
		return
	}

	for dur, url := range CoinexURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, strings.ToUpper(chainType), "USDT"), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Coinex, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)
	coinex = &CoinexExchange{
		CommonExchange: commonExchange,
	}
	return
}

// NewBinance constructs a BinanceExchange.
func NewBTCCoinex(client *http.Client, channels *BotChannels, _ string) (coinex Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(CoinexURLs.Price, "DCR", "BTC"), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(CoinexURLs.Depth, "DCR", "BTC"), nil)
	if err != nil {
		return
	}

	for dur, url := range CoinexURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, "DCR", "BTC"), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(BTCCoinex, client, reqs, channels)
	commonExchange.Symbol = DCRBTCSYMBOL
	coinex = &CoinexExchange{
		CommonExchange: commonExchange,
	}
	return
}

// NewBinance constructs a BinanceExchange.
func NewBinance(client *http.Client, channels *BotChannels, binanceApiUrl string) (binance Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(BinanceURLs.Price, binanceApiUrl, "USDT"), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(BinanceURLs.Depth, binanceApiUrl, "USDT"), nil)
	if err != nil {
		return
	}

	for dur, url := range BinanceURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, binanceApiUrl, "USDT"), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Binance, client, reqs, channels)
	commonExchange.Symbol = DCRUSDSYMBOL
	binance = &BinanceExchange{
		CommonExchange: commonExchange,
	}
	return
}

func NewMexc(client *http.Client, channels *BotChannels, apiURL string) (mexc Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(MexcURLs.Price, apiURL, "USDT"), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(MexcURLs.Depth, apiURL, "USDT"), nil)
	if err != nil {
		return
	}

	for dur, url := range MexcURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, apiURL, "USDT"), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Mexc, client, reqs, channels)
	commonExchange.Symbol = DCRUSDSYMBOL
	mexc = &MexcExchange{
		CommonExchange: commonExchange,
	}
	return
}

func NewXt(client *http.Client, channels *BotChannels, _ string) (xt Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, XtURLs.Price, nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, XtURLs.Depth, nil)
	if err != nil {
		return
	}

	for dur, url := range XtURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Xt, client, reqs, channels)
	commonExchange.Symbol = DCRUSDSYMBOL
	commonExchange.mainCoin = "dcr"
	xt = &XtExchange{
		CommonExchange: commonExchange,
	}
	return
}

func NewPionex(client *http.Client, channels *BotChannels, _ string) (pionex Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, PionexURLs.Price, nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, PionexURLs.Depth, nil)
	if err != nil {
		return
	}

	for dur, url := range PionexURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Pionex, client, reqs, channels)
	commonExchange.Symbol = DCRUSDSYMBOL
	commonExchange.mainCoin = "dcr"
	pionex = &PionexExchange{
		CommonExchange: commonExchange,
	}
	return
}

func MutilchainNewGemini(client *http.Client, channels *BotChannels, chainType string, _ string) (gemini Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(GeminiMutilchainURLs.Price, chainType), nil)
	if err != nil {
		return
	}

	reqs.subprice, err = http.NewRequest(http.MethodGet, fmt.Sprintf(GeminiMutilchainURLs.SubPrice, chainType), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(GeminiMutilchainURLs.Depth, chainType), nil)
	if err != nil {
		return
	}

	for dur, url := range GeminiMutilchainURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, chainType), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(Gemini, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)
	gemini = &GeminiExchange{
		CommonExchange: commonExchange,
	}
	return
}

// NewBinance constructs a BinanceExchange.
func NewKucoin(client *http.Client, channels *BotChannels, _ string) (kucoin Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, KucoinURLs.Price, nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, KucoinURLs.Depth, nil)
	if err != nil {
		return
	}

	for dur, url := range KucoinURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(KuCoin, client, reqs, channels)
	commonExchange.Symbol = DCRUSDSYMBOL
	kucoin = &KucoinExchange{
		CommonExchange: commonExchange,
	}
	return
}

func MutilchainNewKucoin(client *http.Client, channels *BotChannels, chainType string, _ string) (kucoin Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(KucoinMutilchainURLs.Price, strings.ToUpper(chainType)), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(KucoinMutilchainURLs.Depth, strings.ToUpper(chainType)), nil)
	if err != nil {
		return
	}

	for dur, url := range KucoinMutilchainURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, strings.ToUpper(chainType)), nil)
		if err != nil {
			return
		}
	}

	commonExchange := newCommonExchange(KuCoin, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)
	kucoin = &KucoinExchange{
		CommonExchange: commonExchange,
	}
	return
}

type CoinexResponseData struct {
	Market     string `json:"market"`
	Close      string `json:"close"`
	High       string `json:"high"`
	Last       string `json:"last"`
	Low        string `json:"low"`
	Open       string `json:"open"`
	Period     int64  `json:"period"`
	Value      string `json:"value"`
	Volume     string `json:"volume"`
	VolumeBuy  string `json:"volume_buy"`
	VolumeSell string `json:"volume_sell"`
}

type CoinexPriceResponse struct {
	Code    int64                `json:"code"`
	Data    []CoinexResponseData `json:"data"`
	Message string               `json:"message"`
}

type CoinexDepthResponseData struct {
	Depth  CoinexDepthDetailData `json:"depth"`
	IsFull bool                  `json:"is_full"`
	Market string                `json:"market"`
}

type CoinexDepthDetailData struct {
	Asks      [][2]string `json:"asks"`
	Bids      [][2]string `json:"bids"`
	CheckSum  int64       `json:"checksum"`
	Last      string      `json:"last"`
	UpdatedAt int64       `json:"updated_at"`
}

type CoinexDepthResponse struct {
	Code    int64                   `json:"code"`
	Data    CoinexDepthResponseData `json:"data"`
	Message string                  `json:"message"`
}

type CoinexCandlestickResponse struct {
	Code    int64                         `json:"code"`
	Data    []CoinexCandlestickDetailData `json:"data"`
	Message string                        `json:"message"`
}

type CoinexCandlestickDetailData struct {
	Close     string `json:"close"`
	CreatedAt int64  `json:"created_at"`
	High      string `json:"high"`
	Low       string `json:"low"`
	Market    string `json:"market"`
	Open      string `json:"open"`
	Value     string `json:"value"`
	Volume    string `json:"volume"`
}

type KrakenResponseData struct {
	XXMRZUSD KrakenResponseDetail `json:"XXMRZUSD"`
}

type KrakenResponseDetail struct {
	A []string `json:"a"`
	B []string `json:"b"`
	C []string `json:"c"`
	V []string `json:"v"`
	P []string `json:"p"`
	T []int    `json:"t"`
	L []string `json:"l"`
	H []string `json:"h"`
	O string   `json:"o"`
}

type KrakenPriceResponse struct {
	Error  []string           `json:"error"`
	Result KrakenResponseData `json:"result"`
}

type KrakenDepthResponse struct {
	Error  []string                `json:"error"`
	Result KrakenDepthResponseData `json:"result"`
}

type KrakenDepthResponseData struct {
	XXMRZUSD KrakenDepthResponseDetail `json:"XXMRZUSD"`
}

type KrakenDepthResponseDetail struct {
	Asks [][]interface{} `json:"asks"`
	Bids [][]interface{} `json:"bids"`
}

type KrakenCandlesticksResponse struct {
	Error  []string                       `json:"error"`
	Result KrakenCandlesticksResponseData `json:"result"`
}

type KrakenCandlesticksResponseData struct {
	XXMRZUSD [][]interface{} `json:"XXMRZUSD"`
}

// BinancePriceResponse models the JSON price data returned from the Binance API.
type BinancePriceResponse struct {
	Symbol             string `json:"symbol"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	WeightedAvgPrice   string `json:"weightedAvgPrice"`
	PrevClosePrice     string `json:"prevClosePrice"`
	LastPrice          string `json:"lastPrice"`
	LastQty            string `json:"lastQty"`
	BidPrice           string `json:"bidPrice"`
	BidQty             string `json:"bidQty"`
	AskPrice           string `json:"askPrice"`
	AskQty             string `json:"askQty"`
	OpenPrice          string `json:"openPrice"`
	HighPrice          string `json:"highPrice"`
	LowPrice           string `json:"lowPrice"`
	Volume             string `json:"volume"`
	QuoteVolume        string `json:"quoteVolume"`
	OpenTime           int64  `json:"openTime"`
	CloseTime          int64  `json:"closeTime"`
	FirstID            int64  `json:"firstId"`
	LastID             int64  `json:"lastId"`
	Count              int64  `json:"count"`
}

type MexcPriceResponse struct {
	Symbol             string `json:"symbol"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	PrevClosePrice     string `json:"prevClosePrice"`
	LastPrice          string `json:"lastPrice"`
	BidPrice           string `json:"bidPrice"`
	BidQty             string `json:"bidQty"`
	AskPrice           string `json:"askPrice"`
	AskQty             string `json:"askQty"`
	OpenPrice          string `json:"openPrice"`
	HighPrice          string `json:"highPrice"`
	LowPrice           string `json:"lowPrice"`
	Volume             string `json:"volume"`
	QuoteVolume        string `json:"quoteVolume"`
	OpenTime           int64  `json:"openTime"`
	CloseTime          int64  `json:"closeTime"`
	Count              *int64 `json:"count"`
}

type XtTickerResponse struct {
	Result []XtTicker `json:"result"`
	Mc     string     `json:"mc"`
}

type XtTicker struct {
	Symbol      string `json:"s"`
	Time        int64  `json:"t"`
	Vol         string `json:"v"`
	Open        string `json:"o"`
	Low         string `json:"l"`
	High        string `json:"h"`
	Close       string `json:"c"`
	Quantity    string `json:"q"`
	PriceChange string `json:"cv"`
}

type XtCandlestickResponse struct {
	Mc     string              `json:"mc"`
	Result []XtCandlestickItem `json:"result"`
}

type PionexCandlestickResponse struct {
	Result bool                    `json:"result"`
	Data   PionexCandlestickKlines `json:"data"`
}

type PionexCandlestickKlines struct {
	Klines []PionexCandlestickItem `json:"klines"`
}

type PionexCandlestickItem struct {
	Time  int64  `json:"time"`
	Vol   string `json:"volume"`
	Open  string `json:"open"`
	Low   string `json:"low"`
	High  string `json:"high"`
	Close string `json:"close"`
}

type XtCandlestickItem struct {
	Time     int64  `json:"t"`
	Vol      string `json:"v"`
	Open     string `json:"o"`
	Low      string `json:"l"`
	High     string `json:"h"`
	Close    string `json:"c"`
	Quantity string `json:"q"`
}

type PionexTicker struct {
	Symbol      string  `json:"symbol"`
	Time        int64   `json:"time"`
	Vol         string  `json:"volume"`
	Open        string  `json:"open"`
	Low         string  `json:"low"`
	High        string  `json:"high"`
	Close       string  `json:"close"`
	Amount      string  `json:"amount"`
	PriceChange float64 `json:"pr"`
}

type PionexData struct {
	Tickers []PionexTicker `json:"tickers"`
}

type PionexTickerResponse struct {
	Result    bool       `json:"result"`
	Data      PionexData `json:"data"`
	Timestamp int64      `json:"timestamp"`
}

type HotcoinTickerResponse struct {
	Ticker    []HotcoinTickerInner `json:"ticker"`
	Status    string               `json:"status"`
	Timestamp int64                `json:"timestamp"`
}

type HotcoinTickerInner struct {
	Ticker    []HotcoinTicker `json:"ticker"`
	Status    string          `json:"status"`
	Timestamp int64           `json:"timestamp"`
}

type HotcoinTicker struct {
	Symbol string  `json:"symbol"`
	High   float64 `json:"high"`
	Vol    float64 `json:"vol"`
	Last   float64 `json:"last"`
	Low    float64 `json:"low"`
	Buy    float64 `json:"buy"`
	Sell   float64 `json:"sell"`
	Change float64 `json:"change"`
}

type KucoinDepthResponse struct {
	Code string                  `json:"code"`
	Data KucoinDepthResponseData `json:"data"`
}

type KucoinCandlestickResponse struct {
	Code string      `json:"code"`
	Data [][7]string `json:"data"`
}

type KucoinDepthResponseData struct {
	Time     int64       `json:"time"`
	Sequence string      `json:"sequence"`
	Bids     [][2]string `json:"bids"`
	Asks     [][2]string `json:"asks"`
}

type KucoinPriceResponse struct {
	Code string             `json:"code"`
	Data KucoinResponseData `json:"data"`
}

type KucoinResponseData struct {
	Symbol           string `json:"symbol"`
	Time             int64  `json:"time"`
	Buy              string `json:"buy"`
	Sell             string `json:"sell"`
	ChangeRate       string `json:"changeRate"`
	ChangePrice      string `json:"changePrice"`
	High             string `json:"high"`
	Low              string `json:"low"`
	Vol              string `json:"vol"`
	VolValue         string `json:"volValue"`
	Last             string `json:"last"`
	AveragePrice     string `json:"averagePrice"`
	TakerFeeRate     string `json:"takerFeeRate"`
	MakerFeeRate     string `json:"makerFeeRate"`
	TakerCoefficient string `json:"takerCoefficient"`
	MakerCoefficient string `json:"makerCoefficient"`
}

type GeminiPriceResponse struct {
	Symbol  string   `json:"symbol"`
	Open    string   `json:"open"`
	High    string   `json:"high"`
	Low     string   `json:"low"`
	Close   string   `json:"close"`
	Changes []string `json:"changes"`
	Bid     string   `json:"bid"`
	Ask     string   `json:"ask"`
}

type GeminiSubPriceResponse struct {
	Ask    string         `json:"ask"`
	Bid    string         `json:"bid"`
	Last   string         `json:"last"`
	Volume map[string]any `json:"volume"`
}

type GeminiDepthResponse struct {
	Bids []GeminiDepthResData `json:"bids"`
	Asks []GeminiDepthResData `json:"asks"`
}

type GeminiDepthResData struct {
	Price     string `json:"price"`
	Amount    string `json:"amount"`
	Timestamp string `json:"timestamp"`
}

type GeminiCandlestickResponse [][6]any

type BinanceCandlestickResponse [][]interface{}

func badBinanceStickElement(key string, element interface{}) Candlesticks {
	log.Errorf("Unable to decode %s from Binance candlestick: %T: %v", key, element, element)
	return Candlesticks{}
}

func badGeminiStickElement(key string, element interface{}) Candlesticks {
	log.Errorf("Unable to decode %s from Gemini candlestick: %T: %v", key, element, element)
	return Candlesticks{}
}

func badKrakenStickElement(key string, element interface{}) Candlesticks {
	log.Errorf("Unable to decode %s from Kraken candlestick: %T: %v", key, element, element)
	return Candlesticks{}
}

func (r GeminiCandlestickResponse) translate() Candlesticks {
	sticks := make(Candlesticks, 0, len(r))
	for _, rawStick := range r {
		if len(rawStick) < 6 {
			log.Error("Unable to decode Gemini candlestick response. Not enough elements.")
			return Candlesticks{}
		}
		//parse Start time
		startFloat, ok := rawStick[0].(float64)
		if !ok {
			return badGeminiStickElement("Start time", rawStick[0])
		}
		startTime := time.Unix(int64(startFloat/1e3), 0)

		//parse open price
		openPrice, ok := rawStick[1].(float64)
		if !ok {
			return badGeminiStickElement("Open price", rawStick[1])
		}
		highestPrice, ok := rawStick[2].(float64)
		if !ok {
			return badGeminiStickElement("Highest price", rawStick[2])
		}
		lowestPrice, ok := rawStick[3].(float64)
		if !ok {
			return badGeminiStickElement("Lowest price", rawStick[3])
		}
		closePrice, ok := rawStick[4].(float64)
		if !ok {
			return badGeminiStickElement("Close price", rawStick[4])
		}
		volume, ok := rawStick[5].(float64)
		if !ok {
			return badGeminiStickElement("Volume", rawStick[5])
		}

		sticks = append(sticks, Candlestick{
			High:   highestPrice,
			Low:    lowestPrice,
			Open:   openPrice,
			Close:  closePrice,
			Volume: volume,
			Start:  startTime,
		})
	}
	return sticks
}

func (r *KrakenCandlesticksResponse) translate() Candlesticks {
	sticks := make(Candlesticks, 0)
	if r == nil || len(r.Error) > 0 {
		return sticks
	}
	for _, rawStick := range r.Result.XXMRZUSD {
		if len(rawStick) < 8 {
			log.Error("Unable to decode Kraken candlestick response. Not enough elements.")
			return Candlesticks{}
		}
		//parse Start time
		startTimeFloat64, ok := rawStick[0].(float64)
		if !ok {
			return badKrakenStickElement("Start time", rawStick[0])
		}
		startTime := time.Unix(int64(math.Floor(startTimeFloat64)), 0)

		//parse open price
		openPriceStr, ok := rawStick[1].(string)
		if !ok {
			return badKrakenStickElement("Open price", rawStick[1])
		}
		openPrice, err := strconv.ParseFloat(openPriceStr, 64)
		if err != nil {
			log.Warnf("parse open price to float64 failed. %v", err)
			return sticks
		}

		//parse high price
		highPriceStr, ok := rawStick[2].(string)
		if !ok {
			return badKrakenStickElement("High price", rawStick[2])
		}
		highPrice, err := strconv.ParseFloat(highPriceStr, 64)
		if err != nil {
			log.Warnf("parse high price to float64 failed. %v", err)
			return sticks
		}

		//parse low price
		lowPriceStr, ok := rawStick[3].(string)
		if !ok {
			return badKrakenStickElement("Low price", rawStick[3])
		}
		lowPrice, err := strconv.ParseFloat(lowPriceStr, 64)
		if err != nil {
			log.Warnf("parse low price to float64 failed. %v", err)
			return sticks
		}

		//parse close price
		closePriceStr, ok := rawStick[4].(string)
		if !ok {
			return badKrakenStickElement("Close price", rawStick[4])
		}
		closePrice, err := strconv.ParseFloat(closePriceStr, 64)
		if err != nil {
			log.Warnf("parse close price to float64 failed. %v", err)
			return sticks
		}

		//parse volume
		volumeStr, ok := rawStick[6].(string)
		if !ok {
			return badKrakenStickElement("Volume", rawStick[6])
		}
		volume, err := strconv.ParseFloat(volumeStr, 64)
		if err != nil {
			log.Warnf("parse volume to float64 failed. %v", err)
			return sticks
		}

		sticks = append(sticks, Candlestick{
			High:   highPrice,
			Low:    lowPrice,
			Open:   openPrice,
			Close:  closePrice,
			Volume: volume,
			Start:  startTime,
		})
	}
	return sticks
}

func ReverseCandlesticks(input Candlesticks) Candlesticks {
	res := make(Candlesticks, 0)
	for i := len(input) - 1; i >= 0; i-- {
		res = append(res, input[i])
	}
	return res
}

func (r KucoinCandlestickResponse) translate() Candlesticks {
	sticks := make(Candlesticks, 0, len(r.Data))
	for _, rawStick := range r.Data {
		if len(rawStick) < 7 {
			log.Error("Unable to decode Kucoin candlestick response. Not enough elements.")
			return Candlesticks{}
		}
		//parse Start time
		startUnix, err := strconv.ParseInt(rawStick[0], 0, 32)
		if err != nil {
			log.Error("Unable to parse Kucoin Start time: %v", err)
			return Candlesticks{}
		}
		startTime := time.Unix(startUnix, 0)

		//parse open price
		openPrice, err := strconv.ParseFloat(rawStick[1], 64)
		if err != nil {
			log.Error("Unable to parse Kucoin Open Price: %v", err)
			return Candlesticks{}
		}
		//parse close price
		closePrice, err := strconv.ParseFloat(rawStick[2], 64)
		if err != nil {
			log.Error("Unable to parse Kucoin Close Price: %v", err)
			return Candlesticks{}
		}
		//parse highest price
		highestPrice, err := strconv.ParseFloat(rawStick[3], 64)
		if err != nil {
			log.Error("Unable to parse Kucoin Highest Price: %v", err)
			return Candlesticks{}
		}

		//parse lowest price
		lowestPrice, err := strconv.ParseFloat(rawStick[4], 64)
		if err != nil {
			log.Error("Unable to parse Kucoin Lowest Price: %v", err)
			return Candlesticks{}
		}

		//parse Transaction volumn
		volume, err := strconv.ParseFloat(rawStick[5], 64)
		if err != nil {
			log.Error("Unable to parse Kucoin Volume: %v", err)
			return Candlesticks{}
		}

		sticks = append(sticks, Candlestick{
			High:   highestPrice,
			Low:    lowestPrice,
			Open:   openPrice,
			Close:  closePrice,
			Volume: volume,
			Start:  startTime,
		})
	}
	return sticks
}

func (r XtCandlestickResponse) translate() Candlesticks {
	if r.Mc != "SUCCESS" {
		return make(Candlesticks, 0)
	}
	sticks := make(Candlesticks, 0, len(r.Result))
	for _, rawStick := range r.Result {
		startTime := time.Unix(rawStick.Time/1e3, 0)
		open, err := strconv.ParseFloat(rawStick.Open, 64)
		if err != nil {
			return badBinanceStickElement("open float", err)
		}
		high, err := strconv.ParseFloat(rawStick.High, 64)
		if err != nil {
			return badBinanceStickElement("high float", err)
		}
		low, err := strconv.ParseFloat(rawStick.Low, 64)
		if err != nil {
			return badBinanceStickElement("low float", err)
		}
		close, err := strconv.ParseFloat(rawStick.Close, 64)
		if err != nil {
			return badBinanceStickElement("close float", err)
		}
		volume, err := strconv.ParseFloat(rawStick.Quantity, 64)
		if err != nil {
			return badBinanceStickElement("volume float", err)
		}
		sticks = append(sticks, Candlestick{
			High:   high,
			Low:    low,
			Open:   open,
			Close:  close,
			Volume: volume,
			Start:  startTime,
		})
	}
	return sticks
}

func (r PionexCandlestickResponse) translate() Candlesticks {
	if !r.Result {
		return make(Candlesticks, 0)
	}
	sticks := make(Candlesticks, 0, len(r.Data.Klines))
	for _, rawStick := range r.Data.Klines {
		startTime := time.Unix(rawStick.Time/1e3, 0)
		open, err := strconv.ParseFloat(rawStick.Open, 64)
		if err != nil {
			return badBinanceStickElement("open float", err)
		}
		high, err := strconv.ParseFloat(rawStick.High, 64)
		if err != nil {
			return badBinanceStickElement("high float", err)
		}
		low, err := strconv.ParseFloat(rawStick.Low, 64)
		if err != nil {
			return badBinanceStickElement("low float", err)
		}
		close, err := strconv.ParseFloat(rawStick.Close, 64)
		if err != nil {
			return badBinanceStickElement("close float", err)
		}
		volume, err := strconv.ParseFloat(rawStick.Vol, 64)
		if err != nil {
			return badBinanceStickElement("volume float", err)
		}
		sticks = append(sticks, Candlestick{
			High:   high,
			Low:    low,
			Open:   open,
			Close:  close,
			Volume: volume,
			Start:  startTime,
		})
	}
	return sticks
}

func (r HotcoinKlineResponse) translate() Candlesticks {
	sticks := make(Candlesticks, 0, len(r.Data))
	for _, rawStick := range r.Data {
		if len(rawStick) < 6 {
			log.Error("Unable to decode Binance candlestick response. Not enough elements.")
			return Candlesticks{}
		}
		unixMsFlt, ok := rawStick[0].(string)
		if !ok {
			return badBinanceStickElement("start time", rawStick[0])
		}
		timeInt, err := strconv.ParseInt(unixMsFlt, 0, 64)
		if err != nil {
			return badBinanceStickElement("start time", rawStick[0])
		}
		startTime := time.Unix(timeInt/1e3, 0)

		openStr, ok := rawStick[1].(string)
		if !ok {
			return badBinanceStickElement("open", rawStick[1])
		}
		open, err := strconv.ParseFloat(openStr, 64)
		if err != nil {
			return badBinanceStickElement("open float", err)
		}

		highStr, ok := rawStick[2].(string)
		if !ok {
			return badBinanceStickElement("high", rawStick[2])
		}
		high, err := strconv.ParseFloat(highStr, 64)
		if err != nil {
			return badBinanceStickElement("high float", err)
		}

		lowStr, ok := rawStick[3].(string)
		if !ok {
			return badBinanceStickElement("low", rawStick[3])
		}
		low, err := strconv.ParseFloat(lowStr, 64)
		if err != nil {
			return badBinanceStickElement("low float", err)
		}

		closeStr, ok := rawStick[4].(string)
		if !ok {
			return badBinanceStickElement("close", rawStick[4])
		}
		close, err := strconv.ParseFloat(closeStr, 64)
		if err != nil {
			return badBinanceStickElement("close float", err)
		}

		volumeStr, ok := rawStick[5].(string)
		if !ok {
			return badBinanceStickElement("volume", rawStick[5])
		}
		volume, err := strconv.ParseFloat(volumeStr, 64)
		if err != nil {
			return badBinanceStickElement("volume float", err)
		}
		sticks = append(sticks, Candlestick{
			High:   high,
			Low:    low,
			Open:   open,
			Close:  close,
			Volume: volume,
			Start:  startTime,
		})
	}
	return sticks
}

func (r BinanceCandlestickResponse) translate() Candlesticks {
	sticks := make(Candlesticks, 0, len(r))
	for _, rawStick := range r {
		if len(rawStick) < 6 {
			log.Error("Unable to decode Binance candlestick response. Not enough elements.")
			return Candlesticks{}
		}
		unixMsFlt, ok := rawStick[0].(float64)
		if !ok {
			return badBinanceStickElement("start time", rawStick[0])
		}
		startTime := time.Unix(int64(unixMsFlt/1e3), 0)

		openStr, ok := rawStick[1].(string)
		if !ok {
			return badBinanceStickElement("open", rawStick[1])
		}
		open, err := strconv.ParseFloat(openStr, 64)
		if err != nil {
			return badBinanceStickElement("open float", err)
		}

		highStr, ok := rawStick[2].(string)
		if !ok {
			return badBinanceStickElement("high", rawStick[2])
		}
		high, err := strconv.ParseFloat(highStr, 64)
		if err != nil {
			return badBinanceStickElement("high float", err)
		}

		lowStr, ok := rawStick[3].(string)
		if !ok {
			return badBinanceStickElement("low", rawStick[3])
		}
		low, err := strconv.ParseFloat(lowStr, 64)
		if err != nil {
			return badBinanceStickElement("low float", err)
		}

		closeStr, ok := rawStick[4].(string)
		if !ok {
			return badBinanceStickElement("close", rawStick[4])
		}
		close, err := strconv.ParseFloat(closeStr, 64)
		if err != nil {
			return badBinanceStickElement("close float", err)
		}

		volumeStr, ok := rawStick[5].(string)
		if !ok {
			return badBinanceStickElement("volume", rawStick[5])
		}
		volume, err := strconv.ParseFloat(volumeStr, 64)
		if err != nil {
			return badBinanceStickElement("volume float", err)
		}

		sticks = append(sticks, Candlestick{
			High:   high,
			Low:    low,
			Open:   open,
			Close:  close,
			Volume: volume,
			Start:  startTime,
		})
	}
	return sticks
}

// BinanceDepthResponse models the response for Binance depth chart data.
type BinanceDepthResponse struct {
	UpdateID int64
	Bids     [][2]string
	Asks     [][2]string
}

type MexcDepthResponse struct {
	LastUpdateID int         `json:"lastUpdateId"`
	Bids         [][2]string `json:"bids"`
	Asks         [][2]string `json:"asks"`
}

type XtDepthResponse struct {
	Mc     string            `json:"mc"`
	Result XtDepthResultData `json:"result"`
}

type XtDepthResultData struct {
	Symbol    string      `json:"symbol"`
	Timestamp int64       `json:"timestamp"`
	Bids      [][2]string `json:"bids"`
	Asks      [][2]string `json:"asks"`
}

type PionexDepthResponse struct {
	Result bool                  `json:"result"`
	Data   PionexDepthResultData `json:"data"`
}

type PionexDepthResultData struct {
	Bids [][2]string `json:"bids"`
	Asks [][2]string `json:"asks"`
}

type HotcoinFullResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Time int64           `json:"time"`
	Data HotcoinFullData `json:"data"`
}

type HotcoinFullData struct {
	Period HotcoinPeriod `json:"period"`
	Depth  HotcoinDepth  `json:"depth"`
}

type HotcoinPeriod struct {
	Data       string `json:"data"`
	MarketFrom string `json:"marketFrom"`
	Type       int    `json:"type"`
	CoinVol    string `json:"coinVol"`
}

type HotcoinDepth struct {
	Date      int64      `json:"date"`
	Asks      [][]string `json:"asks"`
	Bids      [][]string `json:"bids"`
	LastPrice float64    `json:"lastPrice"`
}

type HotcoinKlineResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Time int64           `json:"time"`
	Data [][]interface{} `json:"data"`
}

func parseKrakenDepthPoints(pts [][]interface{}) ([]DepthPoint, error) {
	outPts := make([]DepthPoint, 0, len(pts))
	for _, pt := range pts {
		var price float64
		var quantity float64
		var err error
		if len(pt) >= 3 {
			if v, ok := pt[0].(string); ok {
				price, err = strconv.ParseFloat(v, 64)
				if err != nil {
					return outPts, err
				}
			}
			if v, ok := pt[1].(string); ok {
				quantity, err = strconv.ParseFloat(v, 64)
				if err != nil {
					return outPts, err
				}
			}
		}
		outPts = append(outPts, DepthPoint{
			Quantity: quantity,
			Price:    price,
		})
	}
	return outPts, nil
}

func parseBinanceDepthPoints(pts [][2]string) ([]DepthPoint, error) {
	outPts := make([]DepthPoint, 0, len(pts))
	for _, pt := range pts {
		price, err := strconv.ParseFloat(pt[0], 64)
		if err != nil {
			return outPts, fmt.Errorf("Unable to parse Binance depth point price: %v", err)
		}

		quantity, err := strconv.ParseFloat(pt[1], 64)
		if err != nil {
			return outPts, fmt.Errorf("Unable to parse Binance depth point quantity: %v", err)
		}

		outPts = append(outPts, DepthPoint{
			Quantity: quantity,
			Price:    price,
		})
	}
	return outPts, nil
}

func parseHotcoinDepthPoints(pts [][]string) ([]DepthPoint, error) {
	outPts := make([]DepthPoint, 0, len(pts))
	for _, pt := range pts {
		price, err := strconv.ParseFloat(pt[0], 64)
		if err != nil {
			return outPts, fmt.Errorf("Unable to parse Binance depth point price: %v", err)
		}

		quantity, err := strconv.ParseFloat(pt[1], 64)
		if err != nil {
			return outPts, fmt.Errorf("Unable to parse Binance depth point quantity: %v", err)
		}

		outPts = append(outPts, DepthPoint{
			Quantity: quantity,
			Price:    price,
		})
	}
	return outPts, nil
}

func (pts *HotcoinFullResponse) translate() *DepthData {
	if pts == nil {
		return nil
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	var err error
	depth.Asks, err = parseHotcoinDepthPoints(pts.Data.Depth.Asks)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	depth.Bids, err = parseHotcoinDepthPoints(pts.Data.Depth.Bids)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	return depth
}

func (r *KrakenDepthResponse) translate() *DepthData {
	if r == nil || len(r.Error) > 0 {
		return nil
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	var err error
	depth.Asks, err = parseKrakenDepthPoints(r.Result.XXMRZUSD.Asks)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	depth.Bids, err = parseKrakenDepthPoints(r.Result.XXMRZUSD.Bids)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	return depth
}

func (r *BinanceDepthResponse) translate() *DepthData {
	if r == nil {
		return nil
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	var err error
	depth.Asks, err = parseBinanceDepthPoints(r.Asks)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	depth.Bids, err = parseBinanceDepthPoints(r.Bids)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	return depth
}

func (r *MexcDepthResponse) translate() *DepthData {
	if r == nil {
		return nil
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	var err error
	depth.Asks, err = parseBinanceDepthPoints(r.Asks)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	depth.Bids, err = parseBinanceDepthPoints(r.Bids)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	return depth
}

func parseGeminiDepthPoints(pts []GeminiDepthResData) ([]DepthPoint, error) {
	outPts := make([]DepthPoint, 0, len(pts))
	for _, pt := range pts {
		price, err := strconv.ParseFloat(pt.Price, 64)
		if err != nil {
			return outPts, fmt.Errorf("Unable to parse Gemini depth point price: %v", err)
		}

		quantity, err := strconv.ParseFloat(pt.Amount, 64)
		if err != nil {
			return outPts, fmt.Errorf("Unable to parse Gemini depth point quantity: %v", err)
		}

		outPts = append(outPts, DepthPoint{
			Quantity: quantity,
			Price:    price,
		})
	}
	return outPts, nil
}

func (r *GeminiDepthResponse) translate() *DepthData {
	if r == nil {
		return nil
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	var err error
	depth.Asks, err = parseGeminiDepthPoints(r.Asks)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	depth.Bids, err = parseGeminiDepthPoints(r.Bids)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	return depth
}

func (r *KucoinDepthResponse) translate() *DepthData {
	if r == nil {
		return nil
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	var err error
	depth.Asks, err = parseBinanceDepthPoints(r.Data.Asks)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	depth.Bids, err = parseBinanceDepthPoints(r.Data.Bids)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	return depth
}

func (r *CoinexDepthResponse) translate() *DepthData {
	if r == nil {
		return nil
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	var err error
	depth.Asks, err = parseBinanceDepthPoints(r.Data.Depth.Asks)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	depth.Bids, err = parseBinanceDepthPoints(r.Data.Depth.Bids)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	return depth
}

func (r *XtDepthResponse) translate() *DepthData {
	if r == nil || r.Mc != "SUCCESS" {
		return nil
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	var err error
	depth.Asks, err = parseBinanceDepthPoints(r.Result.Asks)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	depth.Bids, err = parseBinanceDepthPoints(r.Result.Bids)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	return depth
}

func (r *PionexDepthResponse) translate() *DepthData {
	if r == nil || !r.Result {
		return nil
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	var err error
	depth.Asks, err = parseBinanceDepthPoints(r.Data.Asks)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	depth.Bids, err = parseBinanceDepthPoints(r.Data.Bids)
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}
	return depth
}

func (r CoinexCandlestickResponse) translate() Candlesticks {
	sticks := make(Candlesticks, 0, len(r.Data))
	for _, rawStick := range r.Data {
		//parse Start time
		startTime := time.Unix(rawStick.CreatedAt/1000, 0)

		//parse open price
		openPrice, err := strconv.ParseFloat(rawStick.Open, 64)
		if err != nil {
			log.Error("Unable to parse Coinex Open Price: %v", err)
			return Candlesticks{}
		}
		//parse close price
		closePrice, err := strconv.ParseFloat(rawStick.Close, 64)
		if err != nil {
			log.Error("Unable to parse Coinex Close Price: %v", err)
			return Candlesticks{}
		}
		//parse highest price
		highestPrice, err := strconv.ParseFloat(rawStick.High, 64)
		if err != nil {
			log.Error("Unable to parse Coinex Highest Price: %v", err)
			return Candlesticks{}
		}

		//parse lowest price
		lowestPrice, err := strconv.ParseFloat(rawStick.Low, 64)
		if err != nil {
			log.Error("Unable to parse Coinex Lowest Price: %v", err)
			return Candlesticks{}
		}

		//parse Transaction volumn
		volume, err := strconv.ParseFloat(rawStick.Volume, 64)
		if err != nil {
			log.Error("Unable to parse Coinex Volume: %v", err)
			return Candlesticks{}
		}

		sticks = append(sticks, Candlestick{
			High:   highestPrice,
			Low:    lowestPrice,
			Open:   openPrice,
			Close:  closePrice,
			Volume: volume,
			Start:  startTime,
		})
	}
	return sticks
}

// Refresh retrieves and parses API data from Coinex.
func (kraken *KrakenExchange) Refresh() {
	kraken.LogRequest()
	priceResponse := new(KrakenPriceResponse)
	err := kraken.fetch(kraken.requests.price, priceResponse)
	if err != nil || len(priceResponse.Error) > 0 {
		kraken.fail("Fetch price", err)
		return
	}
	// get price
	priceStr := priceResponse.Result.XXMRZUSD.C[0]
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		kraken.fail(fmt.Sprintf("Failed to parse float from LastPrice=%s", priceStr), err)
		return
	}
	lowPrice, err := strconv.ParseFloat(priceResponse.Result.XXMRZUSD.L[0], 64)
	if err != nil {
		kraken.fail(fmt.Sprintf("Failed to parse float from Low=%s", priceResponse.Result.XXMRZUSD.L[0]), err)
		return
	}
	highPrice, err := strconv.ParseFloat(priceResponse.Result.XXMRZUSD.H[0], 64)
	if err != nil {
		kraken.fail(fmt.Sprintf("Failed to parse float from High=%s", priceResponse.Result.XXMRZUSD.H[0]), err)
		return
	}
	volume, err := strconv.ParseFloat(priceResponse.Result.XXMRZUSD.V[0], 64)
	if err != nil {
		kraken.fail(fmt.Sprintf("Failed to parse float from Volume=%s", priceResponse.Result.XXMRZUSD.V[0]), err)
		return
	}
	quoteVolume := volume * price
	openPrice, err := strconv.ParseFloat(priceResponse.Result.XXMRZUSD.O, 64)
	if err != nil {
		kraken.fail(fmt.Sprintf("Failed to parse float from Open Price=%s", priceResponse.Result.XXMRZUSD.O), err)
		return
	}
	priceChange := price - openPrice

	// Get the depth chart
	depthResponse := new(KrakenDepthResponse)
	err = kraken.fetch(kraken.requests.depth, depthResponse)
	if err != nil {
		log.Errorf("Error retrieving depth chart data from Kraken: %v", err)
	}
	depth := depthResponse.translate()
	// Grab the current state to check if candlesticks need updating
	state := kraken.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range kraken.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", kraken.token, bin)
			response := new(KrakenCandlesticksResponse)
			err := kraken.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from kraken for bin size %s: %v", string(bin), err)
				continue
			}
			sticks := response.translate()

			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}
	kraken.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     kraken.Symbol,
			Price:      price,
			Low:        lowPrice,
			High:       highPrice,
			BaseVolume: volume,
			Volume:     quoteVolume,
			Change:     priceChange,
			Stamp:      time.Now().Unix(),
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

// Refresh retrieves and parses API data from Coinex.
func (bitfinex *BitfinexExchange) Refresh() {
	bitfinex.LogRequest()
	var tickerResponse []float64
	err := bitfinex.fetch(bitfinex.requests.price, &tickerResponse)
	if err != nil || len(tickerResponse) == 0 {
		bitfinex.fail("Fetch price", err)
		return
	}
	if len(tickerResponse) < 10 {
		bitfinex.fail("Ticker response length failed", fmt.Errorf("ticker response length failed"))
		return
	}
	price := tickerResponse[6]
	lowPrice := tickerResponse[9]
	highPrice := tickerResponse[8]
	volume := tickerResponse[7]
	quoteVolume := volume * price
	priceChange := tickerResponse[4]

	// Get the depth chart
	var depthResponse [][]float64
	err = bitfinex.fetch(bitfinex.requests.depth, &depthResponse)
	if err != nil {
		log.Errorf("Error retrieving depth chart data from bitfinex: %v", err)
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	asks := make([]DepthPoint, 0)
	bids := make([]DepthPoint, 0)
	for _, depthItem := range depthResponse {
		if len(depthItem) < 3 {
			continue
		}
		if depthItem[2] > 0 {
			bids = append(bids, DepthPoint{
				Quantity: depthItem[2],
				Price:    depthItem[0],
			})
		} else {
			asks = append(asks, DepthPoint{
				Quantity: 0 - depthItem[2],
				Price:    depthItem[0],
			})
		}
	}
	depth.Asks = asks
	depth.Bids = bids

	// Grab the current state to check if candlesticks need updating
	state := bitfinex.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range bitfinex.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", bitfinex.token, bin)
			var response [][]float64
			err := bitfinex.fetch(req, &response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from bitfinex for bin size %s: %v", string(bin), err)
				continue
			}
			sticks := make(Candlesticks, 0)
			for i := len(response) - 1; i >= 0; i-- {
				candleData := response[i]
				if len(candleData) < 6 {
					continue
				}
				timeSecond := int64(math.Floor(candleData[0])) / 1000
				candleTime := time.Unix(timeSecond, 0)
				sticks = append(sticks, Candlestick{
					Start:  candleTime,
					High:   candleData[3],
					Low:    candleData[4],
					Open:   candleData[1],
					Close:  candleData[2],
					Volume: candleData[5],
				})
			}

			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}
	bitfinex.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     bitfinex.Symbol,
			Price:      price,
			Low:        lowPrice,
			High:       highPrice,
			BaseVolume: volume,
			Volume:     quoteVolume,
			Change:     priceChange,
			Stamp:      time.Now().Unix(),
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

// Refresh retrieves and parses API data from Coinex.
func (coinex *CoinexExchange) Refresh() {
	coinex.LogRequest()
	priceResponse := new(CoinexPriceResponse)
	err := coinex.fetch(coinex.requests.price, priceResponse)
	if err != nil || len(priceResponse.Data) == 0 {
		coinex.fail("Fetch price", err)
		return
	}
	priceRes := priceResponse.Data[0]
	price, err := strconv.ParseFloat(priceRes.Last, 64)
	if err != nil {
		coinex.fail(fmt.Sprintf("Failed to parse float from LastPrice=%s", priceRes.Last), err)
		return
	}
	lowPrice, err := strconv.ParseFloat(priceRes.Low, 64)
	if err != nil {
		coinex.fail(fmt.Sprintf("Failed to parse float from Low=%s", priceRes.Last), err)
		return
	}
	highPrice, err := strconv.ParseFloat(priceRes.High, 64)
	if err != nil {
		coinex.fail(fmt.Sprintf("Failed to parse float from High=%s", priceRes.Last), err)
		return
	}
	quoteVolume, err := strconv.ParseFloat(priceRes.Volume, 64)
	if err != nil {
		coinex.fail(fmt.Sprintf("Failed to parse float from QuoteVolume=%s", priceRes.Volume), err)
		return
	}

	volume := quoteVolume
	openPrice, err := strconv.ParseFloat(priceRes.Open, 64)
	if err != nil {
		coinex.fail(fmt.Sprintf("Failed to parse float from Open Price=%s", priceRes.Open), err)
		return
	}
	priceChange := price - openPrice
	// Get the depth chart
	depthResponse := new(CoinexDepthResponse)
	err = coinex.fetch(coinex.requests.depth, depthResponse)
	if err != nil {
		log.Errorf("Error retrieving depth chart data from Coinex: %v", err)
	}
	depth := depthResponse.translate()

	// Grab the current state to check if candlesticks need updating
	state := coinex.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range coinex.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", coinex.token, bin)
			response := new(CoinexCandlestickResponse)
			err := coinex.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from coinex for bin size %s: %v", string(bin), err)
				continue
			}
			sticks := response.translate()

			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}
	coinex.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     coinex.Symbol,
			Price:      price,
			Low:        lowPrice,
			High:       highPrice,
			BaseVolume: volume,
			Volume:     quoteVolume,
			Change:     priceChange,
			Stamp:      time.Now().Unix(),
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

func (mexc *MexcExchange) Refresh() {
	mexc.LogRequest()
	priceResponse := new(MexcPriceResponse)
	err := mexc.fetch(mexc.requests.price, priceResponse)
	if err != nil {
		mexc.fail("Fetch price", err)
		return
	}
	price, err := strconv.ParseFloat(priceResponse.LastPrice, 64)
	if err != nil {
		mexc.fail(fmt.Sprintf("Failed to parse float from LastPrice=%s", priceResponse.LastPrice), err)
		return
	}
	lowPrice, err := strconv.ParseFloat(priceResponse.LowPrice, 64)
	if err != nil {
		mexc.fail(fmt.Sprintf("Failed to parse float from LowPrice=%s", priceResponse.LowPrice), err)
		return
	}
	highPrice, err := strconv.ParseFloat(priceResponse.HighPrice, 64)
	if err != nil {
		mexc.fail(fmt.Sprintf("Failed to parse float from HighPrice=%s", priceResponse.HighPrice), err)
		return
	}
	quoteVolume, err := strconv.ParseFloat(priceResponse.QuoteVolume, 64)
	if err != nil {
		mexc.fail(fmt.Sprintf("Failed to parse float from QuoteVolume=%s", priceResponse.QuoteVolume), err)
		return
	}

	volume, err := strconv.ParseFloat(priceResponse.Volume, 64)
	if err != nil {
		mexc.fail(fmt.Sprintf("Failed to parse float from Volume=%s", priceResponse.Volume), err)
		return
	}
	priceChange, err := strconv.ParseFloat(priceResponse.PriceChange, 64)
	if err != nil {
		mexc.fail(fmt.Sprintf("Failed to parse float from PriceChange=%s", priceResponse.PriceChange), err)
		return
	}

	// Get the depth chart
	depthResponse := new(MexcDepthResponse)
	err = mexc.fetch(mexc.requests.depth, depthResponse)
	if err != nil {
		log.Errorf("Error retrieving depth chart data from Mexc: %v", err)
	}
	depth := depthResponse.translate()

	// Grab the current state to check if candlesticks need updating
	state := mexc.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range mexc.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", mexc.token, bin)
			response := new(BinanceCandlestickResponse)
			err := mexc.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from mexc for bin size %s: %v", string(bin), err)
				continue
			}
			sticks := response.translate()
			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}
	mexc.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     mexc.Symbol,
			Price:      price,
			Low:        lowPrice,
			High:       highPrice,
			BaseVolume: volume,
			Volume:     quoteVolume,
			Change:     priceChange,
			Stamp:      priceResponse.CloseTime / 1000,
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

// Refresh retrieves and parses API data from xt.com.
func (pionex *PionexExchange) Refresh() {
	pionex.LogRequest()
	priceResponse := new(PionexTickerResponse)
	err := pionex.fetch(pionex.requests.price, priceResponse)
	if err != nil {
		pionex.fail("Fetch price", err)
		return
	}
	if !priceResponse.Result || len(priceResponse.Data.Tickers) <= 0 {
		pionex.fail("fetch price failed", fmt.Errorf("fetch price failed"))
	}
	price, err := strconv.ParseFloat(priceResponse.Data.Tickers[0].Close, 64)
	if err != nil {
		pionex.fail(fmt.Sprintf("Failed to parse float from LastPrice=%s", priceResponse.Data.Tickers[0].Close), err)
		return
	}
	lowPrice, err := strconv.ParseFloat(priceResponse.Data.Tickers[0].Low, 64)
	if err != nil {
		pionex.fail(fmt.Sprintf("Failed to parse float from LowPrice=%s", priceResponse.Data.Tickers[0].Low), err)
		return
	}
	highPrice, err := strconv.ParseFloat(priceResponse.Data.Tickers[0].High, 64)
	if err != nil {
		pionex.fail(fmt.Sprintf("Failed to parse float from HighPrice=%s", priceResponse.Data.Tickers[0].High), err)
		return
	}
	baseVolume, err := strconv.ParseFloat(priceResponse.Data.Tickers[0].Vol, 64)
	if err != nil {
		pionex.fail(fmt.Sprintf("Failed to parse float from basevolume=%s", priceResponse.Data.Tickers[0].Vol), err)
		return
	}
	volume, err := strconv.ParseFloat(priceResponse.Data.Tickers[0].Amount, 64)
	if err != nil {
		pionex.fail(fmt.Sprintf("Failed to parse float from Volume=%s", priceResponse.Data.Tickers[0].Amount), err)
		return
	}

	// Get the depth chart
	depthResponse := new(PionexDepthResponse)
	err = pionex.fetch(pionex.requests.depth, depthResponse)
	if err != nil {
		log.Errorf("Error retrieving depth chart data from Xt.com: %v", err)
	}
	depth := depthResponse.translate()

	// Grab the current state to check if candlesticks need updating
	state := pionex.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range pionex.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", pionex.token, bin)
			response := new(PionexCandlestickResponse)
			err := pionex.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from pionex for bin size %s: %v", string(bin), err)
				continue
			}
			sticks := response.translate()
			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}
	pionex.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     pionex.Symbol,
			Price:      price,
			Low:        lowPrice,
			High:       highPrice,
			BaseVolume: baseVolume,
			Volume:     volume,
			Change:     0.0,
			Stamp:      priceResponse.Data.Tickers[0].Time / 1000,
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

// Refresh retrieves and parses API data from xt.com.
func (xt *XtExchange) Refresh() {
	xt.LogRequest()
	priceResponse := new(XtTickerResponse)
	err := xt.fetch(xt.requests.price, priceResponse)
	if err != nil {
		xt.fail("Fetch price", err)
		return
	}
	if priceResponse.Mc != "SUCCESS" || len(priceResponse.Result) <= 0 {
		xt.fail("fetch price failed", fmt.Errorf("fetch price failed"))
	}
	price, err := strconv.ParseFloat(priceResponse.Result[0].Close, 64)
	if err != nil {
		xt.fail(fmt.Sprintf("Failed to parse float from LastPrice=%s", priceResponse.Result[0].Close), err)
		return
	}
	lowPrice, err := strconv.ParseFloat(priceResponse.Result[0].Low, 64)
	if err != nil {
		xt.fail(fmt.Sprintf("Failed to parse float from LowPrice=%s", priceResponse.Result[0].Low), err)
		return
	}
	highPrice, err := strconv.ParseFloat(priceResponse.Result[0].High, 64)
	if err != nil {
		xt.fail(fmt.Sprintf("Failed to parse float from HighPrice=%s", priceResponse.Result[0].High), err)
		return
	}
	baseVolume, err := strconv.ParseFloat(priceResponse.Result[0].Quantity, 64)
	if err != nil {
		xt.fail(fmt.Sprintf("Failed to parse float from basevolume=%s", priceResponse.Result[0].Quantity), err)
		return
	}

	volume, err := strconv.ParseFloat(priceResponse.Result[0].Vol, 64)
	if err != nil {
		xt.fail(fmt.Sprintf("Failed to parse float from Volume=%s", priceResponse.Result[0].Vol), err)
		return
	}
	priceChange, err := strconv.ParseFloat(priceResponse.Result[0].PriceChange, 64)
	if err != nil {
		xt.fail(fmt.Sprintf("Failed to parse float from PriceChange=%s", priceResponse.Result[0].PriceChange), err)
		return
	}

	// Get the depth chart
	depthResponse := new(XtDepthResponse)
	err = xt.fetch(xt.requests.depth, depthResponse)
	if err != nil {
		log.Errorf("Error retrieving depth chart data from Xt.com: %v", err)
	}
	depth := depthResponse.translate()

	// Grab the current state to check if candlesticks need updating
	state := xt.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range xt.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", xt.token, bin)
			response := new(XtCandlestickResponse)
			err := xt.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from xt for bin size %s: %v", string(bin), err)
				continue
			}
			sticks := response.translate()
			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}
	xt.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     xt.Symbol,
			Price:      price,
			Low:        lowPrice,
			High:       highPrice,
			BaseVolume: baseVolume,
			Volume:     volume,
			Change:     priceChange,
			Stamp:      priceResponse.Result[0].Time / 1000,
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

func (hotcoin *HotcoinExchange) Refresh() {
	hotcoin.LogRequest()
	priceResponse := new(HotcoinTickerResponse)
	err := hotcoin.fetch(hotcoin.requests.price, priceResponse)
	if err != nil || priceResponse.Status != "ok" || len(priceResponse.Ticker) <= 0 || len(priceResponse.Ticker[0].Ticker) <= 0 {
		hotcoin.fail("Fetch price", err)
		return
	}
	// get token data
	mainTicker := priceResponse.Ticker[0].Ticker[0]
	if mainTicker.Symbol == "" {
		hotcoin.fail("Not exist hotcoin data", fmt.Errorf("not exist hotcoin data"))
		return
	}
	// Get the depth chart
	depthResponse := new(HotcoinFullResponse)
	err = hotcoin.fetch(hotcoin.requests.depth, depthResponse)
	if err != nil {
		log.Errorf("Error retrieving depth chart data from hotcoin: %v", err)
	}
	depth := depthResponse.translate()

	// Grab the current state to check if candlesticks need updating
	state := hotcoin.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range hotcoin.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", hotcoin.token, bin)
			response := new(HotcoinKlineResponse)
			err := hotcoin.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from hotcoin for bin size %s: %v", string(bin), err)
				continue
			}
			sticks := response.translate()
			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}
	hotcoin.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     hotcoin.Symbol,
			Price:      mainTicker.Last,
			Low:        mainTicker.Low,
			High:       mainTicker.High,
			BaseVolume: mainTicker.Vol,
			Volume:     mainTicker.Vol * mainTicker.Last,
			Change:     mainTicker.Change,
			Stamp:      priceResponse.Timestamp,
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

// Refresh retrieves and parses API data from Binance.
func (binance *BinanceExchange) Refresh() {
	binance.LogRequest()
	priceResponse := new(BinancePriceResponse)
	err := binance.fetch(binance.requests.price, priceResponse)
	if err != nil {
		binance.fail("Fetch price", err)
		return
	}
	price, err := strconv.ParseFloat(priceResponse.LastPrice, 64)
	if err != nil {
		binance.fail(fmt.Sprintf("Failed to parse float from LastPrice=%s", priceResponse.LastPrice), err)
		return
	}
	lowPrice, err := strconv.ParseFloat(priceResponse.LowPrice, 64)
	if err != nil {
		binance.fail(fmt.Sprintf("Failed to parse float from LowPrice=%s", priceResponse.LowPrice), err)
		return
	}
	highPrice, err := strconv.ParseFloat(priceResponse.HighPrice, 64)
	if err != nil {
		binance.fail(fmt.Sprintf("Failed to parse float from HighPrice=%s", priceResponse.HighPrice), err)
		return
	}
	quoteVolume, err := strconv.ParseFloat(priceResponse.QuoteVolume, 64)
	if err != nil {
		binance.fail(fmt.Sprintf("Failed to parse float from QuoteVolume=%s", priceResponse.QuoteVolume), err)
		return
	}

	volume, err := strconv.ParseFloat(priceResponse.Volume, 64)
	if err != nil {
		binance.fail(fmt.Sprintf("Failed to parse float from Volume=%s", priceResponse.Volume), err)
		return
	}
	priceChange, err := strconv.ParseFloat(priceResponse.PriceChange, 64)
	if err != nil {
		binance.fail(fmt.Sprintf("Failed to parse float from PriceChange=%s", priceResponse.PriceChange), err)
		return
	}

	// Get the depth chart
	depthResponse := new(BinanceDepthResponse)
	err = binance.fetch(binance.requests.depth, depthResponse)
	if err != nil {
		log.Errorf("Error retrieving depth chart data from Binance: %v", err)
	}
	depth := depthResponse.translate()

	// Grab the current state to check if candlesticks need updating
	state := binance.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range binance.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", binance.token, bin)
			response := new(BinanceCandlestickResponse)
			err := binance.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from binance for bin size %s: %v", string(bin), err)
				continue
			}
			sticks := response.translate()

			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}
	binance.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     binance.Symbol,
			Price:      price,
			Low:        lowPrice,
			High:       highPrice,
			BaseVolume: volume,
			Volume:     quoteVolume,
			Change:     priceChange,
			Stamp:      priceResponse.CloseTime / 1000,
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

// Refresh retrieves and parses API data from Binance.
func (kucoin *KucoinExchange) Refresh() {
	kucoin.LogRequest()
	priceResponse := new(KucoinPriceResponse)
	err := kucoin.fetch(kucoin.requests.price, priceResponse)
	if err != nil {
		kucoin.fail("Fetch price", err)
		return
	}
	price, err := strconv.ParseFloat(priceResponse.Data.Last, 64)
	if err != nil {
		kucoin.fail(fmt.Sprintf("Failed to parse float from LastPrice=%s", priceResponse.Data.Last), err)
		return
	}
	lowPrice, err := strconv.ParseFloat(priceResponse.Data.Low, 64)
	if err != nil {
		kucoin.fail(fmt.Sprintf("Failed to parse float from Low=%s", priceResponse.Data.Low), err)
		return
	}
	highPrice, err := strconv.ParseFloat(priceResponse.Data.High, 64)
	if err != nil {
		kucoin.fail(fmt.Sprintf("Failed to parse float from High=%s", priceResponse.Data.High), err)
		return
	}
	volumeValue, err := strconv.ParseFloat(priceResponse.Data.VolValue, 64)
	if err != nil {
		kucoin.fail(fmt.Sprintf("Failed to parse float from BaseVolume=%s", priceResponse.Data.VolValue), err)
		return
	}

	volume, err := strconv.ParseFloat(priceResponse.Data.Vol, 64)
	if err != nil {
		kucoin.fail(fmt.Sprintf("Failed to parse float from Volume=%s", priceResponse.Data.Vol), err)
		return
	}
	priceChange, err := strconv.ParseFloat(priceResponse.Data.ChangePrice, 64)
	if err != nil {
		kucoin.fail(fmt.Sprintf("Failed to parse float from PriceChange=%s", priceResponse.Data.ChangePrice), err)
		return
	}

	// Get the depth chart
	depthResponse := new(KucoinDepthResponse)
	err = kucoin.fetch(kucoin.requests.depth, depthResponse)
	if err != nil {
		log.Errorf("Error retrieving depth chart data from Kucoin: %v", err)
	}
	depth := depthResponse.translate()

	// Grab the current state to check if candlesticks need updating
	state := kucoin.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range kucoin.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", kucoin.token, bin)
			response := new(KucoinCandlestickResponse)
			err := kucoin.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from kucoin for bin size %s: %v", string(bin), err)
				continue
			}
			sticks := response.translate()
			sticks = ReverseCandlesticks(sticks)
			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}
	kucoin.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     kucoin.Symbol,
			Price:      price,
			Low:        lowPrice,
			High:       highPrice,
			BaseVolume: volume,
			Volume:     volumeValue,
			Change:     priceChange,
			Stamp:      priceResponse.Data.Time,
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

func (gemini *GeminiExchange) Refresh() {
	gemini.LogRequest()
	priceResponse := new(GeminiPriceResponse)
	err := gemini.fetch(gemini.requests.price, priceResponse)
	if err != nil {
		gemini.fail("Fetch price", err)
		return
	}

	subPriceResponse := new(GeminiSubPriceResponse)
	err = gemini.fetch(gemini.requests.subprice, subPriceResponse)
	if err != nil {
		gemini.fail("Fetch price", err)
		return
	}

	price, err := strconv.ParseFloat(subPriceResponse.Last, 64)
	if err != nil {
		gemini.fail(fmt.Sprintf("Failed to parse float from LastPrice=%s", subPriceResponse.Last), err)
		return
	}
	volumne := float64(0)
	baseVolume := float64(0)
	timeStamp := int64(0)
	//check subpriceresponse
	for key := range subPriceResponse.Volume {
		if key == "timestamp" {
			timeStampFloat64, ok := subPriceResponse.Volume[key].(float64)
			if !ok {
				badGeminiStickElement("Start time", subPriceResponse.Volume[key])
				return
			}
			timeStamp = int64(timeStampFloat64 / 1e3)
		} else {
			//if symbol start with key, is base volume
			if strings.HasPrefix(gemini.Symbol, strings.ToUpper(key)) {
				baseVolStr := subPriceResponse.Volume[key].(string)
				baseVolume, err = strconv.ParseFloat(baseVolStr, 64)
				if err != nil {
					gemini.fail(fmt.Sprintf("Failed to parse float from Base Volume=%s", baseVolStr), err)
					return
				}
				//else, is volumn
			} else {
				volStr := subPriceResponse.Volume[key].(string)
				volumne, err = strconv.ParseFloat(volStr, 64)
				if err != nil {
					gemini.fail(fmt.Sprintf("Failed to parse float from Volume=%s", volStr), err)
					return
				}
			}
		}
	}

	totalPriceChange := float64(0)
	totalCountChange := int(0)
	for _, change := range priceResponse.Changes {
		if change == "" {
			continue
		}
		hourChangeVal, err := strconv.ParseFloat(change, 64)
		if err != nil {
			continue
		}
		totalCountChange++
		totalPriceChange += hourChangeVal
	}

	changeAvgPrice := float64(0)
	if totalCountChange > 0 {
		changeAvgPrice = totalPriceChange / float64(totalCountChange)
	}

	changePrice := changeAvgPrice - price

	// Get the depth chart
	depthResponse := new(GeminiDepthResponse)
	err = gemini.fetch(gemini.requests.depth, depthResponse)
	if err != nil {
		log.Errorf("Error retrieving depth chart data from Gemini: %v", err)
	}
	depth := depthResponse.translate()

	// Grab the current state to check if candlesticks need updating
	state := gemini.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range gemini.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", gemini.token, bin)
			response := new(GeminiCandlestickResponse)
			err := gemini.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from gemini for bin size %s: %v", string(bin), err)
				continue
			}
			sticks := response.translate()
			sticks = ReverseCandlesticks(sticks)
			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}
	gemini.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     gemini.Symbol,
			Price:      price,
			BaseVolume: baseVolume,
			Volume:     volumne,
			Change:     changePrice,
			Stamp:      timeStamp,
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

// DragonExchange is a Singapore-based crytocurrency exchange.
type DragonExchange struct {
	*CommonExchange
	SymbolID         int
	depthBuyRequest  *http.Request
	depthSellRequest *http.Request
}

// NewDragonEx constructs a DragonExchange.
func NewDragonEx(client *http.Client, channels *BotChannels, _ string) (dragonex Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, DragonExURLs.Price, nil)
	if err != nil {
		return
	}

	// Dragonex has separate endpoints for buy and sell, so the requests are
	// stored as fields of DragonExchange
	var depthSell, depthBuy *http.Request
	depthSell, err = http.NewRequest(http.MethodGet, fmt.Sprintf(DragonExURLs.Depth, "sell"), nil)
	if err != nil {
		return
	}

	depthBuy, err = http.NewRequest(http.MethodGet, fmt.Sprintf(DragonExURLs.Depth, "buy"), nil)
	if err != nil {
		return
	}

	for dur, url := range DragonExURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return
		}
	}

	dragonex = &DragonExchange{
		CommonExchange:   newCommonExchange(DragonEx, client, reqs, channels),
		SymbolID:         1520101,
		depthBuyRequest:  depthBuy,
		depthSellRequest: depthSell,
	}
	return
}

// DragonExResponse models the generic fields returned in every response.
type DragonExResponse struct {
	Ok   bool   `json:"ok"`
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// DragonExPriceResponse models the JSON data returned from the DragonEx API.
type DragonExPriceResponse struct {
	DragonExResponse
	Data []DragonExPriceResponseData `json:"data"`
}

// DragonExPriceResponseData models the JSON data from the DragonEx API.
// Dragonex has the current price in close_price
type DragonExPriceResponseData struct {
	ClosePrice      string `json:"close_price"`
	CurrentVolume   string `json:"current_volume"`
	MaxPrice        string `json:"max_price"`
	MinPrice        string `json:"min_price"`
	OpenPrice       string `json:"open_price"`
	PriceBase       string `json:"price_base"`
	PriceChange     string `json:"price_change"`
	PriceChangeRate string `json:"price_change_rate"`
	Timestamp       int64  `json:"timestamp"`
	TotalAmount     string `json:"total_amount"`
	TotalVolume     string `json:"total_volume"`
	UsdtVolume      string `json:"usdt_amount"`
	SymbolID        int    `json:"symbol_id"`
}

// DragonExDepthPt models a single point of data in a Dragon Exchange depth
// chart data set.
type DragonExDepthPt struct {
	Price  string `json:"price"`
	Volume string `json:"volume"`
}

// DragonExDepthArray is a slice of DragonExDepthPt.
type DragonExDepthArray []DragonExDepthPt

func (pts DragonExDepthArray) translate() []DepthPoint {
	outPts := make([]DepthPoint, 0, len(pts))
	for _, pt := range pts {
		price, err := strconv.ParseFloat(pt.Price, 64)
		if err != nil {
			log.Errorf("DragonExDepthArray.translate failed to parse float from %s", pt.Price)
			return []DepthPoint{}
		}

		volume, err := strconv.ParseFloat(pt.Volume, 64)
		if err != nil {
			log.Errorf("DragonExDepthArray.translate failed to parse volume from %s", pt.Volume)
			return []DepthPoint{}
		}
		outPts = append(outPts, DepthPoint{
			Quantity: volume,
			Price:    price,
		})
	}
	return outPts
}

// DragonExDepthResponse models the Dragon Exchange depth chart data response.
type DragonExDepthResponse struct {
	DragonExResponse
	Data DragonExDepthArray `json:"data"`
}

// DragonExCandlestickColumns models the column list returned in a candlestick
// chart data response from Dragon Exchange.
type DragonExCandlestickColumns []string

func (keys DragonExCandlestickColumns) index(dxKey string) (int, error) {
	for idx, key := range keys {
		if key == dxKey {
			return idx, nil
		}
	}
	return -1, fmt.Errorf("Unable to locate DragonEx candlestick key %s", dxKey)
}

const (
	dxHighKey   = "max_price"
	dxLowKey    = "min_price"
	dxOpenKey   = "open_price"
	dxCloseKey  = "close_price"
	dxVolumeKey = "volume"
	dxTimeKey   = "timestamp"
)

// DragonExCandlestickList models the value list returned in a candlestick
// chart data response from Dragon Exchange.
type DragonExCandlestickList []interface{}

func (list DragonExCandlestickList) getFloat(idx int) (float64, error) {
	if len(list) < idx+1 {
		return -1, fmt.Errorf("DragonEx candlestick point index %d out of range", idx)
	}
	valStr, ok := list[idx].(string)
	if !ok {
		return -1, fmt.Errorf("DragonEx.getFloat found unexpected type at index %d", idx)
	}
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return -1, fmt.Errorf("DragonEx candlestick parseFloat error: %v", err)
	}
	return val, nil
}

// DragonExCandlestickPts is a list of DragonExCandlestickList.
type DragonExCandlestickPts []DragonExCandlestickList

// DragonExCandlestickData models the Data field of DragonExCandlestickResponse.
type DragonExCandlestickData struct {
	Columns DragonExCandlestickColumns `json:"columns"`
	Lists   DragonExCandlestickPts     `json:"lists"`
}

func badDragonexStickElement(key string, err error) Candlesticks {
	log.Errorf("Unable to decode %s from Binance candlestick: %v", key, err)
	return Candlesticks{}
}

func (data DragonExCandlestickData) translate( /*cKey candlestickKey*/ ) Candlesticks {
	sticks := make(Candlesticks, 0, len(data.Lists))
	var idx int
	var err error
	for _, pt := range data.Lists {
		idx, err = data.Columns.index(dxHighKey)
		if err != nil {
			return badDragonexStickElement(dxHighKey, err)
		}
		high, err := pt.getFloat(idx)
		if err != nil {
			return badDragonexStickElement(dxHighKey, err)
		}

		idx, err = data.Columns.index(dxLowKey)
		if err != nil {
			return badDragonexStickElement(dxLowKey, err)
		}
		low, err := pt.getFloat(idx)
		if err != nil {
			return badDragonexStickElement(dxLowKey, err)
		}

		idx, err = data.Columns.index(dxOpenKey)
		if err != nil {
			return badDragonexStickElement(dxOpenKey, err)
		}
		open, err := pt.getFloat(idx)
		if err != nil {
			return badDragonexStickElement(dxOpenKey, err)
		}

		idx, err = data.Columns.index(dxCloseKey)
		if err != nil {
			return badDragonexStickElement(dxCloseKey, err)
		}
		close, err := pt.getFloat(idx)
		if err != nil {
			return badDragonexStickElement(dxCloseKey, err)
		}

		idx, err = data.Columns.index(dxVolumeKey)
		if err != nil {
			return badDragonexStickElement(dxVolumeKey, err)
		}
		volume, err := pt.getFloat(idx)
		if err != nil {
			return badDragonexStickElement(dxVolumeKey, err)
		}

		idx, err = data.Columns.index(dxTimeKey)
		if err != nil {
			return badDragonexStickElement(dxTimeKey, err)
		}
		if len(pt) < idx+1 {
			return badDragonexStickElement(dxTimeKey, fmt.Errorf("DragonEx time index %d out of range", idx))
		}
		unixFloat, ok := pt[idx].(float64)
		if !ok {
			return badDragonexStickElement(dxTimeKey, fmt.Errorf("DragonEx found unexpected type for time at index %d", idx))
		}
		startTime := time.Unix(int64(unixFloat), 0)

		sticks = append(sticks, Candlestick{
			High:   high,
			Low:    low,
			Open:   open,
			Close:  close,
			Volume: volume,
			Start:  startTime,
		})
	}
	return sticks
}

// DragonExCandlestickResponse models the response from DragonEx for the
// historical k-line data.
type DragonExCandlestickResponse struct {
	DragonExResponse
	Data DragonExCandlestickData
}

func (dragonex *DragonExchange) getDragonExDepthData(req *http.Request, response *DragonExDepthResponse) error {
	err := dragonex.fetch(req, response)
	if err != nil {
		return fmt.Errorf("DragonEx buy order book response error: %v", err)
	}
	if !response.Ok {
		return fmt.Errorf("DragonEx depth response server error with message: %s", response.Msg)
	}
	return nil
}

// Refresh retrieves and parses API data from DragonEx.
func (dragonex *DragonExchange) Refresh() {
	dragonex.LogRequest()
	response := new(DragonExPriceResponse)
	err := dragonex.fetch(dragonex.requests.price, response)
	if err != nil {
		dragonex.fail("Fetch", err)
		return
	}
	if !response.Ok {
		dragonex.fail("Response not ok", err)
		return
	}
	if len(response.Data) == 0 {
		dragonex.fail("No data", fmt.Errorf("Response data array is empty"))
		return
	}
	data := response.Data[0]
	if data.SymbolID != dragonex.SymbolID {
		dragonex.fail("Wrong code", fmt.Errorf("Pair id %d in response is not the expected id %d", data.SymbolID, dragonex.SymbolID))
		return
	}
	price, err := strconv.ParseFloat(data.ClosePrice, 64)
	if err != nil {
		dragonex.fail(fmt.Sprintf("Failed to parse float from ClosePrice=%s", data.ClosePrice), err)
		return
	}
	volume, err := strconv.ParseFloat(data.TotalVolume, 64)
	if err != nil {
		dragonex.fail(fmt.Sprintf("Failed to parse float from TotalVolume=%s", data.TotalVolume), err)
		return
	}
	btcVolume := volume * price
	priceChange, err := strconv.ParseFloat(data.PriceChange, 64)
	if err != nil {
		dragonex.fail(fmt.Sprintf("Failed to parse float from PriceChange=%s", data.PriceChange), err)
		return
	}

	// Depth chart
	depthSellResponse := new(DragonExDepthResponse)
	sellErr := dragonex.getDragonExDepthData(dragonex.depthSellRequest, depthSellResponse)
	if sellErr != nil {
		log.Errorf("DragonEx sell order book response error: %v", sellErr)
	}

	depthBuyResponse := new(DragonExDepthResponse)
	buyErr := dragonex.getDragonExDepthData(dragonex.depthBuyRequest, depthBuyResponse)
	if buyErr != nil {
		log.Errorf("DragonEx buy order book response error: %v", buyErr)
	}

	var depth *DepthData
	if sellErr == nil && buyErr == nil {
		depth = &DepthData{
			Time: time.Now().Unix(),
			Bids: depthBuyResponse.Data.translate(),
			Asks: depthSellResponse.Data.translate(),
		}
	}

	// Grab the current state to check if candlesticks need updating
	state := dragonex.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range dragonex.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", dragonex.token, bin)
			response := new(DragonExCandlestickResponse)
			err := dragonex.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from dragonex for bin size %s: %v", string(bin), err)
				continue
			}
			if !response.Ok {
				log.Errorf("DragonEx server error while fetching candlestick data. Message: %s", response.Msg)
			}

			sticks := response.Data.translate()
			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}

	dragonex.Update(&ExchangeState{
		BaseState: BaseState{
			Price:      price,
			BaseVolume: btcVolume,
			Volume:     volume,
			Change:     priceChange,
			Stamp:      data.Timestamp,
		},
		Depth:        depth,
		Candlesticks: candlesticks,
	})
}

// HuobiExchange is based in Hong Kong and Singapore.
type HuobiExchange struct {
	*CommonExchange
	Ok string
}

// NewHuobi constructs a HuobiExchange.
func NewHuobi(client *http.Client, channels *BotChannels, _ string) (huobi Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, HuobiURLs.Price, nil)
	if err != nil {
		return
	}
	reqs.price.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	reqs.depth, err = http.NewRequest(http.MethodGet, HuobiURLs.Depth, nil)
	if err != nil {
		return
	}
	reqs.depth.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	for dur, url := range HuobiURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return
		}
		reqs.candlesticks[dur].Header.Add("Content-Type", "application/x-www-form-urlencoded")
	}
	commonExchange := newCommonExchange(Huobi, client, reqs, channels)
	commonExchange.Symbol = DCRUSDSYMBOL
	return &HuobiExchange{
		CommonExchange: commonExchange,
		Ok:             "ok",
	}, nil
}

func MutilchainNewHuobi(client *http.Client, channels *BotChannels, chainType string, _ string) (huobi Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(HuobiMutilchainURLs.Price, chainType), nil)
	if err != nil {
		return
	}
	reqs.price.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(HuobiMutilchainURLs.Depth, chainType), nil)
	if err != nil {
		return
	}
	reqs.depth.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	for dur, url := range HuobiMutilchainURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, chainType), nil)
		if err != nil {
			return
		}
		reqs.candlesticks[dur].Header.Add("Content-Type", "application/x-www-form-urlencoded")
	}

	commonExchange := newCommonExchange(Huobi, client, reqs, channels)
	commonExchange.Symbol = GetSymbolFromChainType(chainType)

	return &HuobiExchange{
		CommonExchange: commonExchange,
		Ok:             "ok",
	}, nil
}

func GetSymbolFromChainType(chainType string) string {
	switch chainType {
	case TYPEBTC:
		return BTCSYMBOL
	case TYPELTC:
		return LTCSYMBOL
	case TYPEXMR:
		return XMRSYMBOL
	default:
		return DCRUSDSYMBOL
	}
}

// HuobiResponse models the common response fields in all API BittrexResponseResult
type HuobiResponse struct {
	Status string `json:"status"`
	Ch     string `json:"ch"`
	Ts     int64  `json:"ts"`
}

// HuobiPriceTick models the "tick" field of the Huobi API response.
type HuobiPriceTick struct {
	Amount  float64   `json:"amount"`
	Open    float64   `json:"open"`
	Close   float64   `json:"close"`
	High    float64   `json:"high"`
	ID      int64     `json:"id"`
	Count   int64     `json:"count"`
	Low     float64   `json:"low"`
	Version int64     `json:"version"`
	Ask     []float64 `json:"ask"`
	Vol     float64   `json:"vol"`
	Bid     []float64 `json:"bid"`
}

// HuobiPriceResponse models the JSON data returned from the Huobi API.
type HuobiPriceResponse struct {
	HuobiResponse
	Tick HuobiPriceTick `json:"tick"`
}

// HuobiDepthPts is a list of tuples [price, volume].
type HuobiDepthPts [][2]float64

func (pts HuobiDepthPts) translate() []DepthPoint {
	outPts := make([]DepthPoint, 0, len(pts))
	for _, pt := range pts {
		outPts = append(outPts, DepthPoint{
			Quantity: pt[1],
			Price:    pt[0],
		})
	}
	return outPts
}

// HuobiDepthTick models the tick field of the Huobi depth chart response.
type HuobiDepthTick struct {
	ID   int64         `json:"id"`
	Ts   int64         `json:"ts"`
	Bids HuobiDepthPts `json:"bids"`
	Asks HuobiDepthPts `json:"asks"`
}

// HuobiDepthResponse models the response from a Huobi API depth chart response.
type HuobiDepthResponse struct {
	HuobiResponse
	Tick HuobiDepthTick `json:"tick"`
}

// HuobiCandlestickPt is a single candlestick pt in a Huobi API candelstick
// response.
type HuobiCandlestickPt struct {
	ID     int64   `json:"id"` // ID is actually start time as unix stamp
	Open   float64 `json:"open"`
	Close  float64 `json:"close"`
	Low    float64 `json:"low"`
	High   float64 `json:"high"`
	Amount float64 `json:"amount"` // Volume BTC
	Vol    float64 `json:"vol"`    // Volume DCR
	Count  int64   `json:"count"`
}

// HuobiCandlestickData is a list of candlestick data pts.
type HuobiCandlestickData []*HuobiCandlestickPt

func (pts HuobiCandlestickData) translate() Candlesticks {
	sticks := make(Candlesticks, 0, len(pts))
	// reverse the order
	for i := len(pts) - 1; i >= 0; i-- {
		pt := pts[i]
		avgPrice := (pt.High + pt.Low + pt.Open + pt.Close) / 4
		sticks = append(sticks, Candlestick{
			High:   pt.High,
			Low:    pt.Low,
			Open:   pt.Open,
			Close:  pt.Close,
			Volume: pt.Vol / avgPrice,
			Start:  time.Unix(pt.ID, 0),
		})
	}
	return sticks
}

// HuobiCandlestickResponse models the response from Huobi for candlestick data.
type HuobiCandlestickResponse struct {
	HuobiResponse
	Data HuobiCandlestickData `json:"data"`
}

// Refresh retrieves and parses API data from Huobi.
func (huobi *HuobiExchange) Refresh() {
	huobi.LogRequest()
	priceResponse := new(HuobiPriceResponse)
	err := huobi.fetch(huobi.requests.price, priceResponse)
	if err != nil {
		huobi.fail("Fetch", err)
		return
	}
	if priceResponse.Status != huobi.Ok {
		huobi.fail("Status not ok", fmt.Errorf("Expected status %s. Received %s", huobi.Ok, priceResponse.Status))
		return
	}
	volume := priceResponse.Tick.Vol

	// Depth data
	var depth *DepthData
	depthResponse := new(HuobiDepthResponse)
	err = huobi.fetch(huobi.requests.depth, depthResponse)
	if err != nil {
		log.Errorf("Huobi depth chart fetch error: %v", err)
	} else if depthResponse.Status != huobi.Ok {
		log.Errorf("Huobi server depth response error. status: %s", depthResponse.Status)
	} else {
		depth = &DepthData{
			Time: depthResponse.Ts / 1000,
			Bids: depthResponse.Tick.Bids.translate(),
			Asks: depthResponse.Tick.Asks.translate(),
		}
	}

	// Candlestick data
	state := huobi.state()
	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range huobi.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", huobi.token, bin)
			response := new(HuobiCandlestickResponse)
			err := huobi.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from huobi for bin size %s: %v", string(bin), err)
				continue
			}
			if response.Status != huobi.Ok {
				log.Errorf("Huobi server error while fetching candlestick data. status: %s", response.Status)
				continue
			}

			sticks := response.Data.translate()
			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}

	huobi.Update(&ExchangeState{
		BaseState: BaseState{
			Symbol:     huobi.Symbol,
			Price:      priceResponse.Tick.Close,
			Low:        priceResponse.Tick.Low,
			High:       priceResponse.Tick.High,
			BaseVolume: volume / priceResponse.Tick.Close,
			Volume:     volume,
			Change:     priceResponse.Tick.Close - priceResponse.Tick.Open,
			Stamp:      priceResponse.Ts / 1000,
		},
		Depth:        depth,
		Candlesticks: candlesticks,
	})
}

// PoloniexExchange is a U.S.-based exchange.
type PoloniexExchange struct {
	*CommonExchange
	CurrencyPair string
	orderSeq     int64
}

// NewPoloniex constructs a PoloniexExchange.
func NewPoloniex(client *http.Client, channels *BotChannels, _ string) (poloniex Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, PoloniexURLs.Price, nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, PoloniexURLs.Depth, nil)
	if err != nil {
		return
	}

	for dur, url := range PoloniexURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return
		}
	}

	p := &PoloniexExchange{
		CommonExchange: newCommonExchange(Poloniex, client, reqs, channels),
		CurrencyPair:   "BTC_DCR",
	}
	go func() {
		<-channels.done
		ws, _ := p.websocket()
		if ws != nil {
			ws.Close()
		}
	}()
	poloniex = p
	return
}

func MutilchainNewPoloniex(client *http.Client, channels *BotChannels, chainType string, _ string) (poloniex Exchange, err error) {
	reqs := newRequests()
	reqs.price, err = http.NewRequest(http.MethodGet, fmt.Sprintf(PoloniexMutilchainURLs.Price, strings.ToUpper(chainType)), nil)
	if err != nil {
		return
	}

	reqs.depth, err = http.NewRequest(http.MethodGet, fmt.Sprintf(PoloniexMutilchainURLs.Depth, strings.ToUpper(chainType)), nil)
	if err != nil {
		return
	}

	for dur, url := range PoloniexMutilchainURLs.Candlesticks {
		reqs.candlesticks[dur], err = http.NewRequest(http.MethodGet, fmt.Sprintf(url, strings.ToUpper(chainType)), nil)
		if err != nil {
			return
		}
	}

	p := &PoloniexExchange{
		CommonExchange: newCommonExchange(Poloniex, client, reqs, channels),
		CurrencyPair:   fmt.Sprintf("USDT_%s", strings.ToUpper(chainType)),
	}
	go func() {
		<-channels.done
		ws, _ := p.websocket()
		if ws != nil {
			ws.Close()
		}
	}()
	poloniex = p
	return
}

// PoloniexPair models the data returned from the Poloniex API.
type PoloniexPair struct {
	ID            int    `json:"id"`
	Last          string `json:"last"`
	LowestAsk     string `json:"lowestAsk"`
	HighestBid    string `json:"highestBid"`
	PercentChange string `json:"percentChange"`
	BaseVolume    string `json:"baseVolume"`
	QuoteVolume   string `json:"quoteVolume"`
	IsFrozen      string `json:"isFrozen"`
	High24hr      string `json:"high24hr"`
	Low24hr       string `json:"low24hr"`
}

// PoloniexDepthPt is a tuple of ["price", volume].
type PoloniexDepthPt [2]interface{}

func (pt *PoloniexDepthPt) price() (float64, error) {
	pStr, ok := pt[0].(string)
	if !ok {
		return -1, fmt.Errorf("Poloniex depth price translation type error. Failed to parse string from %v, type %T", pt[0], pt[0])
	}
	price, err := strconv.ParseFloat(pStr, 64)
	if err != nil {
		return -1, fmt.Errorf("Poloniex depth price parseFloat error: %v", err)
	}
	return price, nil
}

func (pt *PoloniexDepthPt) volume() (float64, error) {
	volume, ok := pt[1].(float64)
	if !ok {
		return -1, fmt.Errorf("Poloniex depth volume translation type error. Failed to parse float from %v, type %T", pt[0], pt[0])
	}
	return volume, nil
}

// PoloniexDepthArray is a slice of depth chart data points.
type PoloniexDepthArray []*PoloniexDepthPt

func (pts PoloniexDepthArray) translate() ([]DepthPoint, error) {
	outPts := make([]DepthPoint, 0, len(pts))
	for _, pt := range pts {
		price, err := pt.price()
		if err != nil {
			return []DepthPoint{}, err
		}

		volume, err := pt.volume()
		if err != nil {
			return []DepthPoint{}, err
		}

		outPts = append(outPts, DepthPoint{
			Quantity: volume,
			Price:    price,
		})
	}
	return outPts, nil
}

// PoloniexDepthResponse models the response from Poloniex for depth chart data.
type PoloniexDepthResponse struct {
	Asks     PoloniexDepthArray `json:"asks"`
	Bids     PoloniexDepthArray `json:"bids"`
	IsFrozen string             `json:"isFrozen"`
	Seq      int64              `json:"seq"`
}

func (r *PoloniexDepthResponse) translate() *DepthData {
	if r == nil {
		return nil
	}
	depth := new(DepthData)
	depth.Time = time.Now().Unix()
	var err error
	depth.Asks, err = r.Asks.translate()
	if err != nil {
		log.Errorf("%v")
		return nil
	}

	depth.Bids, err = r.Bids.translate()
	if err != nil {
		log.Errorf("%v", err)
		return nil
	}

	return depth
}

// PoloniexCandlestickResponse models the k-line data response from Poloniex.
// {"date":1463356800,"high":1,"low":0.0037,"open":1,"close":0.00432007,"volume":357.23057396,"quoteVolume":76195.11422729,"weightedAverage":0.00468836}

type PoloniexCandlestickPt struct {
	Date            int64   `json:"date"`
	High            float64 `json:"high"`
	Low             float64 `json:"low"`
	Open            float64 `json:"open"`
	Close           float64 `json:"close"`
	Volume          float64 `json:"volume"`
	QuoteVolume     float64 `json:"quoteVolume"`
	WeightedAverage float64 `json:"weightedAverage"`
}

type PoloniexCandlestickResponse []*PoloniexCandlestickPt

func (r PoloniexCandlestickResponse) translate( /*bin candlestickKey*/ ) Candlesticks {
	sticks := make(Candlesticks, 0, len(r))
	for _, stick := range r {
		sticks = append(sticks, Candlestick{
			High:   stick.High,
			Low:    stick.Low,
			Open:   stick.Open,
			Close:  stick.Close,
			Volume: stick.QuoteVolume,
			Start:  time.Unix(stick.Date, 0),
		})
	}
	return sticks
}

// All poloniex websocket subscriptions messages have this form.
type poloniexWsSubscription struct {
	Command string `json:"command"`
	Channel int    `json:"channel"`
}

var poloniexOrderbookSubscription = poloniexWsSubscription{
	Command: "subscribe",
	Channel: 162,
}

// The final structure to parse in the initial websocket message is a map of the
// form {"12.3456":"23.4567", "12.4567":"123.4567", ...} where the price is
// a string-float and is the key to string-float volumes.
func (poloniex *PoloniexExchange) parseOrderMap(book map[string]interface{}, orders wsOrders) error {
	for p, v := range book {
		price, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return fmt.Errorf("Failed to parse float from poloniex orderbook price. given %s: %v", p, err)
		}
		vStr, ok := v.(string)
		if !ok {
			return fmt.Errorf("Failed to cast poloniex orderbook volume to string. given %s", v)
		}
		volume, err := strconv.ParseFloat(vStr, 64)
		if err != nil {
			return fmt.Errorf("Failed to parse float from poloniex orderbook volume string. given %s: %v", p, err)
		}
		binKey := eightPtKey(price)
		orders[binKey] = &wsOrder{
			price:  price,
			volume: volume,
		}
	}
	return nil
}

// This initial websocket message is a full orderbook.
func (poloniex *PoloniexExchange) processWsOrderbook(sequenceID int64, responseList []interface{}) {
	subList, ok := responseList[0].([]interface{})
	if !ok {
		poloniex.setWsFail(fmt.Errorf("Failed to parse 0th element of poloniex response array"))
		return
	}
	if len(subList) < 2 {
		poloniex.setWsFail(fmt.Errorf("Unexpected sub-list length in poloniex websocket response: %d", len(subList)))
		return
	}
	d, ok := subList[1].(map[string]interface{})
	if !ok {
		poloniex.setWsFail(fmt.Errorf("Failed to parse response map from poloniex websocket response"))
		return
	}
	orderBook, ok := d["orderBook"].([]interface{})
	if !ok {
		poloniex.setWsFail(fmt.Errorf("Failed to parse orderbook list from poloniex websocket response"))
		return
	}
	if len(orderBook) < 2 {
		poloniex.setWsFail(fmt.Errorf("Unexpected orderBook list length in poloniex websocket response: %d", len(subList)))
		return
	}
	asks, ok := orderBook[0].(map[string]interface{})
	if !ok {
		poloniex.setWsFail(fmt.Errorf("Failed to parse asks from poloniex orderbook"))
		return
	}

	buys, ok := orderBook[1].(map[string]interface{})
	if !ok {
		poloniex.setWsFail(fmt.Errorf("Failed to parse buys from poloniex orderbook"))
		return
	}

	poloniex.orderMtx.Lock()
	defer poloniex.orderMtx.Unlock()
	poloniex.orderSeq = sequenceID
	err := poloniex.parseOrderMap(asks, poloniex.asks)
	if err != nil {
		poloniex.setWsFail(err)
		return
	}

	err = poloniex.parseOrderMap(buys, poloniex.buys)
	if err != nil {
		poloniex.setWsFail(err)
		return
	}
	poloniex.wsInitialized()
}

// A helper for merging a source map into a target map. Poloniex order in the
// source map with volume 0 will trigger a deletion from the target map.
func mergePoloniexDepthUpdates(target, source wsOrders) {
	for bin, pt := range source {
		if pt.volume == 0 {
			delete(source, bin)
			delete(target, bin)
			continue
		}
		target[bin] = pt
	}
}

// Merge order updates under a write lock.
func (poloniex *PoloniexExchange) accumulateOrders(sequenceID int64, asks, buys wsOrders) {
	poloniex.orderMtx.Lock()
	defer poloniex.orderMtx.Unlock()
	poloniex.orderSeq++
	if sequenceID != poloniex.orderSeq {
		poloniex.setWsFail(fmt.Errorf("poloniex sequence id failure. expected %d, received %d", poloniex.orderSeq, sequenceID))
		return
	}
	mergePoloniexDepthUpdates(poloniex.asks, asks)
	mergePoloniexDepthUpdates(poloniex.buys, buys)
}

const (
	poloniexHeartbeatCode       = 1010
	poloniexInitialOrderbookKey = "i"
	poloniexOrderUpdateKey      = "o"
	poloniexTradeUpdateKey      = "t"
	poloniexAskDirection        = 0
	poloniexBuyDirection        = 1
)

// Poloniex has a string code in the result array indicating what type of
// message it is.
func firstCode(responseList []interface{}) string {
	firstElement, ok := responseList[0].([]interface{})
	if !ok {
		log.Errorf("parse failure in poloniex websocket message")
		return ""
	}
	if len(firstElement) < 1 {
		log.Errorf("unexpected number of parameters in poloniex websocket message")
		return ""
	}
	updateType, ok := firstElement[0].(string)
	if !ok {
		log.Errorf("failed to type convert poloniex message update type")
		return ""
	}
	return updateType
}

// For Poloniex message "o", an update to the orderbook.
func processPoloniexOrderbookUpdate(updateParams []interface{}) (*wsOrder, int, error) {
	floatDir, ok := updateParams[1].(float64)
	if !ok {
		return nil, -1, fmt.Errorf("failed to type convert poloniex orderbook update direction")
	}
	direction := int(floatDir)
	priceStr, ok := updateParams[2].(string)
	if !ok {
		return nil, -1, fmt.Errorf("failed to type convert poloniex orderbook update price")
	}
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to convert poloniex orderbook update price to float: %v", err)
	}
	volStr, ok := updateParams[3].(string)
	if !ok {
		return nil, -1, fmt.Errorf("failed to type convert poloniex orderbook update volume")
	}
	volume, err := strconv.ParseFloat(volStr, 64)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to convert poloniex orderbook update volume to float: %v", err)
	}
	return &wsOrder{
		price:  price,
		volume: volume,
	}, direction, nil
}

// For Poloniex message "t", a trade. This seems to be used rarely and
// sporadically, but it is used. For the BTC_DCR endpoint, almost all updates
// are of the "o" type, an orderbook update. The docs are unclear about whether a trade updates the
// order book, but testing seems to indicate that a "t" message is for trades
// that occur off of the orderbook.
/*
func (poloniex *PoloniexExchange) processTrade(tradeParams []interface{}) (*wsOrder, int, error) {
	if len(tradeParams) != 6 {
		return nil, -1, fmt.Errorf("Not enough parameters in poloniex trade notification. given: %d", len(tradeParams))
	}
	floatDir, ok := tradeParams[2].(float64)
	if !ok {
		return nil, -1, fmt.Errorf("failed to type convert poloniex orderbook update direction")
	}
	direction := (int(floatDir) + 1) % 2
	priceStr, ok := tradeParams[3].(string)
	if !ok {
		return nil, -1, fmt.Errorf("failed to type convert poloniex orderbook update price")
	}
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to convert poloniex orderbook update price to float: %v", err)
	}
	volStr, ok := tradeParams[4].(string)
	if !ok {
		return nil, -1, fmt.Errorf("failed to type convert poloniex orderbook update volume")
	}
	volume, err := strconv.ParseFloat(volStr, 64)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to convert poloniex orderbook update volume to float: %v", err)
	}
	trade := &wsOrder{
		price:  price,
		volume: volume,
	}
	return trade, direction, nil
}
*/

// Poloniex's WebsocketProcessor. Handles messages of type "i", "o", and "t".
func (poloniex *PoloniexExchange) processWsMessage(raw []byte) {
	msg := make([]interface{}, 0)
	err := json.Unmarshal(raw, &msg)
	if err != nil {
		poloniex.setWsFail(err)
		return
	}
	switch len(msg) {
	case 1:
		// Likely a heatbeat
		code, ok := msg[0].(float64)
		if !ok {
			poloniex.setWsFail(fmt.Errorf("non-integer single-element poloniex response of implicit type %T", msg[0]))
			return
		}
		intCode := int(code)
		if intCode == poloniexHeartbeatCode {
			return
		}
		poloniex.setWsFail(fmt.Errorf("unknown code in single-element poloniex response: %d", intCode))
		return
	case 3:
		responseList, ok := msg[2].([]interface{})
		if !ok {
			poloniex.setWsFail(fmt.Errorf("poloniex websocket message type assertion failure: %T", msg[2]))
			return
		}

		if len(responseList) == 0 {
			poloniex.setWsFail(fmt.Errorf("zero-length response list received from poloniex"))
			return
		}

		code := firstCode(responseList)
		rawSeq, ok := msg[1].(float64)
		if !ok {
			poloniex.setWsFail(fmt.Errorf("poloniex websocket sequence id type assertion failure: %T", msg[2]))
			return
		}
		seq := int64(rawSeq)

		if code == poloniexInitialOrderbookKey {
			poloniex.processWsOrderbook(seq, responseList)
			state := poloniex.state()
			if state != nil { // Only send update if price has been fetched
				depth := poloniex.wsDepths()
				poloniex.Update(&ExchangeState{
					BaseState: BaseState{
						Price:      state.Price,
						BaseVolume: state.BaseVolume,
						Volume:     state.Volume,
						Change:     state.Change,
					},
					Depth:        depth,
					Candlesticks: state.Candlesticks,
				})
			}
			return
		}

		if code != poloniexOrderUpdateKey && code != poloniexTradeUpdateKey {
			poloniex.setWsFail(fmt.Errorf("Unexpected code in first element of poloniex websocket response list: %s", code))
			return
		}

		newAsks := make(wsOrders)
		newBids := make(wsOrders)
		var count int
		for _, update := range responseList {
			updateParams, ok := update.([]interface{})
			if !ok {
				poloniex.setWsFail(fmt.Errorf("failed to type convert poloniex orderbook update array"))
				return
			}
			if len(updateParams) < 4 {
				poloniex.setWsFail(fmt.Errorf("unexpected number of parameters in poloniex orderboook update"))
				return
			}
			updateType, ok := updateParams[0].(string)
			if !ok {
				poloniex.setWsFail(fmt.Errorf("failed to type convert poloniex orderbook update type"))
				return
			}

			var order *wsOrder
			var direction int
			if updateType == poloniexOrderUpdateKey {
				order, direction, err = processPoloniexOrderbookUpdate(updateParams)
				if err != nil {
					poloniex.setWsFail(err)
				}
			} else if updateType == poloniexTradeUpdateKey {
				continue
				// trade, direction, err = poloniex.processTrade(updateParams)
			}

			switch direction {
			case poloniexAskDirection:
				newAsks[eightPtKey(order.price)] = order
			case poloniexBuyDirection:
				newBids[eightPtKey(order.price)] = order
			default:
				poloniex.setWsFail(fmt.Errorf("Unknown poloniex update direction indicator: %d", direction))
				return
			}
			count++
		}
		poloniex.accumulateOrders(seq, newAsks, newBids)
		if count > 0 {
			poloniex.wsUpdated()
		}
	default:
		poloniex.setWsFail(fmt.Errorf("poloniex websocket message had unexpected length %d", len(msg)))
		return
	}
}

// Create a websocket connection and send the orderbook subscription.
func (poloniex *PoloniexExchange) connectWs() {
	err := poloniex.connectWebsocket(poloniex.processWsMessage, &socketConfig{
		address: PoloniexURLs.Websocket,
	})
	if err != nil {
		log.Errorf("connectWs: %v", err)
		return
	}
	err = poloniex.wsSend(poloniexOrderbookSubscription)
	if err != nil {
		log.Errorf("Failed to send order book sub to polo: %v", err)
	}
}

// Refresh retrieves and parses API data from Poloniex.
func (poloniex *PoloniexExchange) Refresh() {
	poloniex.LogRequest()

	var response map[string]*PoloniexPair
	err := poloniex.fetch(poloniex.requests.price, &response)
	if err != nil {
		poloniex.fail("Fetch", err)
		return
	}
	market, ok := response[poloniex.CurrencyPair]
	if !ok {
		poloniex.fail("Market not in response", fmt.Errorf("Response did not have expected CurrencyPair %s", poloniex.CurrencyPair))
		return
	}
	price, err := strconv.ParseFloat(market.Last, 64)
	if err != nil {
		poloniex.fail(fmt.Sprintf("Failed to parse float from Last=%s", market.Last), err)
		return
	}
	baseVolume, err := strconv.ParseFloat(market.BaseVolume, 64)
	if err != nil {
		poloniex.fail(fmt.Sprintf("Failed to parse float from BaseVolume=%s", market.BaseVolume), err)
		return
	}
	volume, err := strconv.ParseFloat(market.QuoteVolume, 64)
	if err != nil {
		poloniex.fail(fmt.Sprintf("Failed to parse float from QuoteVolume=%s", market.QuoteVolume), err)
		return
	}
	percentChange, err := strconv.ParseFloat(market.PercentChange, 64)
	if err != nil {
		poloniex.fail(fmt.Sprintf("Failed to parse float from PercentChange=%s", market.PercentChange), err)
		return
	}
	oldPrice := price / (1 + percentChange)

	// Check for a depth chart from the websocket orderbook.
	tryHttp, wsStarting, depth := poloniex.wsDepthStatus(poloniex.connectWs)

	// If not expecting depth data from the websocket, grab it from HTTP
	if tryHttp {
		depthResponse := new(PoloniexDepthResponse)
		err = poloniex.fetch(poloniex.requests.depth, depthResponse)
		if err != nil {
			log.Errorf("Poloniex depth chart fetch error: %v", err)
		}
		depth = depthResponse.translate()
	}

	if !wsStarting {
		sinceLast := time.Since(poloniex.wsLastUpdate())
		log.Tracef("last bittrex websocket update %.3f seconds ago", sinceLast.Seconds())
		if sinceLast > depthDataExpiration && !poloniex.wsFailed() {
			poloniex.setWsFail(fmt.Errorf("lost connection detected. bittrex websocket will reconnect during next refresh"))
		}
	}

	// Candlesticks
	state := poloniex.state()

	candlesticks := map[candlestickKey]Candlesticks{}
	for bin, req := range poloniex.requests.candlesticks {
		oldSticks, found := state.Candlesticks[bin]
		if !found || oldSticks.needsUpdate(bin) {
			log.Tracef("Signalling candlestick update for %s, bin size %s", poloniex.token, bin)
			response := new(PoloniexCandlestickResponse)
			err := poloniex.fetch(req, response)
			if err != nil {
				log.Errorf("Error retrieving candlestick data from poloniex for bin size %s: %v", string(bin), err)
				continue
			}

			sticks := response.translate()
			if !found || sticks.time().After(oldSticks.time()) {
				candlesticks[bin] = sticks
			}
		}
	}

	update := &ExchangeState{
		BaseState: BaseState{
			Price:      price,
			BaseVolume: baseVolume,
			Volume:     volume,
			Change:     price - oldPrice,
		},
		Depth:        depth,
		Candlesticks: candlesticks,
	}
	if wsStarting {
		poloniex.SilentUpdate(update)
	} else {
		poloniex.Update(update)
	}
}

// dexDotDecredMsgID is used as an atomic counter for msgjson.Message IDs.
var dexDotDecredMsgID uint64 = 1

// dexSubscription is the DEX request for the order book feed.
var dexSubscription = &msgjson.OrderBookSubscription{
	Base:  42, // BIP44 coin ID for Decred
	Quote: 0,  // Bitcoin
}

// DEXConfig is the configuration for the Decred DEX server.
type DEXConfig struct {
	Token    string
	Host     string
	Cert     []byte
	CertHost string
}

// candleCache embeds *candles.Cache and adds some fields for internal
// handling.
type candleCache struct {
	*dexcandles.Cache
	mtx       sync.RWMutex
	lastStamp uint64
	key       candlestickKey
}

// DecredDEX is a Decred DEX.
type DecredDEX struct {
	*CommonExchange
	ords         map[string]*msgjson.BookOrderNote
	reqMtx       sync.Mutex
	reqs         map[uint64]func(*msgjson.Message)
	cacheMtx     sync.RWMutex
	candleCaches map[uint64]*candleCache
	seq          uint64
	stamp        int64
	cfg          *DEXConfig
}

// NewDecredDEXConstructor creates a constructor for a DEX with the provided
// configuration.
func NewDecredDEXConstructor(cfg *DEXConfig) func(*http.Client, *BotChannels, string) (Exchange, error) {
	return func(client *http.Client, channels *BotChannels, _ string) (Exchange, error) {
		dcr := &DecredDEX{
			CommonExchange: newCommonExchange(cfg.Token, client, requests{}, channels),
			candleCaches:   make(map[uint64]*candleCache),
			reqs:           make(map[uint64]func(*msgjson.Message)),
			cfg:            cfg,
		}
		go func() {
			<-channels.done
			ws, _ := dcr.websocket()
			if ws != nil {
				ws.Close()
			}
		}()
		return dcr, nil
	}
}

// Refresh grabs a book snapshot and sends the exchange update.
func (dcr *DecredDEX) Refresh() {
	dcr.LogRequest()
	// Check for a depth chart from the websocket orderbook.
	tryHTTP, wsStarting, depth := dcr.wsDepthStatus(dcr.connectWs)
	if tryHTTP {
		log.Debugf("Failed to get WebSocket depth chart for %s", dcr.cfg.Host)
		return
	}
	if wsStarting {
		// Do nothing in this case. We'll update the bot when we get some data.
		return
	}
	price := depth.MidGap()
	candlesticks := make(map[candlestickKey]Candlesticks)
	var change float64
	var volume, bestVolDur uint64
	var aDayMS uint64 = 86400 * 1000
	// Ugh. I need to export the CandleCache.candles.
	for binSize, cache := range dcr.candles() {
		cache.mtx.RLock()
		wc := cache.WireCandles(dexcandles.CacheSize)
		sticks := make(Candlesticks, 0, len(wc.EndStamps))
		for i := range wc.EndStamps {
			sticks = append(sticks, Candlestick{
				High:   float64(wc.HighRates[i]) / 1e8,
				Low:    float64(wc.LowRates[i]) / 1e8,
				Open:   float64(wc.StartRates[i]) / 1e8,
				Close:  float64(wc.EndRates[i]) / 1e8,
				Volume: float64(wc.MatchVolumes[i]) / 1e8,
				Start:  time.Unix(int64(wc.StartStamps[i]/1000), 0),
			})
		}
		cache.mtx.RUnlock()

		candlesticks[cache.key] = sticks
		deepEnough := binSize*dexcandles.CacheSize > aDayMS
		if bestVolDur == 0 || (binSize < bestVolDur && deepEnough) {
			bestVolDur = binSize
			change, volume, _, _ = cache.Delta(time.Now().Add(-time.Hour * 24))
			// Consistent with other APIs' change data storage (Change is the difference in price, not the ratio)
			change = change * price
		}
	}
	vol := float64(volume) / 1e8
	dcr.Update(&ExchangeState{
		BaseState: BaseState{
			Price:      price,
			Change:     change,
			Volume:     vol,
			BaseVolume: vol,
			Stamp:      dcr.lastStamp(),
		},
		Candlesticks: candlesticks,
		Depth:        depth,
	})
}

// candles gets a copy of the candleCaches map.
func (dcr *DecredDEX) candles() map[uint64]*candleCache {
	dcr.cacheMtx.RLock()
	defer dcr.cacheMtx.RUnlock()
	cs := make(map[uint64]*candleCache, len(dcr.candleCaches))
	for binSize, cache := range dcr.candleCaches {
		cs[binSize] = cache
	}
	return cs
}

// clearCandleCache clears the candle cache for the specified bin size.
func (dcr *DecredDEX) clearCandleCache(binSize uint64) {
	dcr.cacheMtx.Lock()
	defer dcr.cacheMtx.Unlock()
	delete(dcr.candleCaches, binSize)
}

// setCandleCache sets the candle cache for the specified bin size.
func (dcr *DecredDEX) setCandleCache(binSize uint64, cache *candleCache) {
	dcr.cacheMtx.Lock()
	defer dcr.cacheMtx.Unlock()
	dcr.candleCaches[binSize] = cache
}

// logRequest stores the response handler for the request ID.
func (dcr *DecredDEX) logRequest(id uint64, handler func(*msgjson.Message)) {
	dcr.reqMtx.Lock()
	defer dcr.reqMtx.Unlock()
	dcr.reqs[id] = handler
}

// responseHandler retrieves and deletes the response handler from the reqs map.
func (dcr *DecredDEX) responseHandler(id uint64) func(*msgjson.Message) {
	dcr.reqMtx.Lock()
	defer dcr.reqMtx.Unlock()
	f := dcr.reqs[id]
	delete(dcr.reqs, id)
	return f
}

// request sends a request, and records the handler for the response.
func (dcr *DecredDEX) request(route string, payload interface{}, handler func(*msgjson.Message)) (uint64, error) {
	msg, _ := msgjson.NewRequest(atomic.AddUint64(&dexDotDecredMsgID, 1), route, payload)
	dcr.logRequest(msg.ID, handler)

	err := dcr.wsSend(msg)
	if err != nil {
		return 0, fmt.Errorf("error sending %s request to %q: %v", route, dcr.cfg.Host, err)
	}
	return msg.ID, nil
}

// Create a websocket connection and send the orderbook subscription.
func (dcr *DecredDEX) connectWs() {
	// Configure TLS.
	if len(dcr.cfg.Cert) == 0 {
		dcr.setWsFail(fmt.Errorf("failed to find certificate for %s", dcr.cfg.CertHost))
		return
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(dcr.cfg.Cert); !ok {
		dcr.setWsFail(fmt.Errorf("invalid certificate"))
		return
	}

	err := dcr.connectWebsocket(dcr.processWsMessage, &socketConfig{
		address: "wss://" + dcr.cfg.Host + "/ws",
		tlsConfig: &tls.Config{
			RootCAs:    pool,
			ServerName: dcr.cfg.CertHost,
		},
	})
	if err != nil {
		dcr.setWsFail(fmt.Errorf("dcr.connectWs: %v", err))
		return
	}

	// Get 'config' to get current bin sizes.
	_, err = dcr.request(msgjson.ConfigRoute, nil, dcr.handleConfigResponse)
	if err != nil {
		dcr.setWsFail(err)
		return
	}

	_, err = dcr.request(msgjson.OrderBookRoute, dexSubscription, dcr.handleSubResponse)
	if err != nil {
		dcr.setWsFail(err)
		return
	}
}

// processWsMessage is DecredDEX's WebsocketProcessor. Handles messages of type
// *msgjson.Message.
func (dcr *DecredDEX) processWsMessage(raw []byte) {
	msg, err := msgjson.DecodeMessage(raw)
	if err != nil {
		dcr.setWsFail(fmt.Errorf("DecodeMessage error: %v", err))
		return
	}

	dcr.orderMtx.Lock()
	defer dcr.orderMtx.Unlock()
	dcr.stamp = time.Now().Unix()
	switch msg.Type {
	case msgjson.Response:
		handler := dcr.responseHandler(msg.ID)
		if handler != nil {
			handler(msg)
		} else {
			log.Warnf("Received response from %q with no request handler registered: %s", dcr.cfg.Host, string(raw))
		}

	case msgjson.Notification:
		switch msg.Route {
		case msgjson.BookOrderRoute:
			bookOrder := new(msgjson.BookOrderNote)
			err := msg.Unmarshal(bookOrder)
			if err != nil {
				dcr.setWsFail(fmt.Errorf("book_order Unmarshal error: %v", err))
				return
			}
			if !dcr.checkSeq(bookOrder.Seq) {
				return
			}
			dcr.bookOrder(bookOrder)
		case msgjson.UnbookOrderRoute:
			unbookOrder := new(msgjson.UnbookOrderNote)
			err := msg.Unmarshal(unbookOrder)
			if err != nil {
				dcr.setWsFail(fmt.Errorf("unbook_order Unmarshal error: %v", err))
				return
			}
			if !dcr.checkSeq(unbookOrder.Seq) {
				return
			}
			dcr.unbookOrder(unbookOrder)
		case msgjson.UpdateRemainingRoute:
			update := new(msgjson.UpdateRemainingNote)
			err := msg.Unmarshal(update)
			if err != nil {
				dcr.setWsFail(fmt.Errorf("update_remaining Unmarshal error: %v", err))
				return
			}
			if !dcr.checkSeq(update.Seq) {
				return
			}
			dcr.updateRemaining(update)
		case msgjson.EpochOrderRoute:
			// We don't actually track epoch orders, but we need to progress the
			// sequence.
			note := new(msgjson.EpochOrderNote)
			err := msg.Unmarshal(note)
			if err != nil {
				dcr.setWsFail(fmt.Errorf("epoch_order Unmarshal error: %v", err))
				return
			}
			dcr.checkSeq(note.Seq)
			return // Skip wsUpdate. Nothing has changed.
		case msgjson.SuspensionRoute:
			note := new(msgjson.TradeSuspension)
			err := msg.Unmarshal(note)
			if err != nil {
				dcr.setWsFail(fmt.Errorf("suspension Unmarshal error: %v", err))
				return
			}
			if note.Persist {
				return
			}
			dcr.checkSeq(note.Seq)
			dcr.clearOrderBook()
		case msgjson.EpochReportRoute:
			note := new(msgjson.EpochReportNote)
			err := msg.Unmarshal(note)
			if err != nil {
				dcr.setWsFail(fmt.Errorf("epoch_report Unmarshal error: %v", err))
				return
			}
			// EpochReportNote.Candle is a value, not a pointer.
			if note.Candle.EndStamp == 0 {
				return
			}
			candle := &note.Candle
			for binSize, cache := range dcr.candles() {
				cache.mtx.Lock()
				if cache.lastStamp == note.StartStamp {
					cache.Add(candle)
					cache.lastStamp = candle.EndStamp
					cache.mtx.Unlock()
				} else {
					// Our candles are out of sync. Get a fresh set.
					log.Infof("Epoch report out of sync (last stamp %d, note start stamp %d). Requesting new candles.",
						cache.lastStamp, note.StartStamp)
					cacheKey := cache.key
					cache.mtx.Unlock()
					dcr.clearCandleCache(binSize)
					_, err := dcr.request(msgjson.CandlesRoute, &msgjson.CandlesRequest{
						BaseID:     42,
						QuoteID:    0,
						BinSize:    (time.Duration(binSize) * time.Millisecond).String(),
						NumCandles: dexcandles.CacheSize,
					}, func(msg *msgjson.Message) {
						dcr.handleCandles(cacheKey, msg)
					})
					if err != nil {
						dcr.setWsFail(fmt.Errorf("error requesting candles for bin size %d: %w", binSize, err))
						break
					}
				}
			}
		}
	}
	dcr.wsUpdated()
}

// handleSubResponse handles the response to the order book subscription.
func (dcr *DecredDEX) handleSubResponse(msg *msgjson.Message) {
	ob := new(msgjson.OrderBook)
	err := msg.UnmarshalResult(ob)
	if err != nil {
		dcr.setWsFail(fmt.Errorf("error unmarshaling orderbook response: %v", err))
		return
	}
	dcr.setOrderBook(ob)
}

// handleCandles handles the response for a set of candles from the data API.
func (dcr *DecredDEX) handleCandles(key candlestickKey, msg *msgjson.Message) {
	wireCandles := new(msgjson.WireCandles)
	err := msg.UnmarshalResult(wireCandles)
	if err != nil {
		log.Errorf("error encountered in candlestick response from DEX at %s: %v", dcr.cfg.Host, err)
		return
	}

	binSize := uint64(key.duration().Milliseconds())

	candles := wireCandles.Candles()

	cache := &candleCache{
		Cache: dexcandles.NewCache(len(candles), binSize),
		key:   key,
	}

	for _, candle := range candles {
		cache.Add(candle)
	}
	if len(candles) > 0 {
		cache.lastStamp = candles[len(candles)-1].EndStamp
	}
	dcr.setCandleCache(binSize, cache)
}

// handleConfigResponse handles the response for the DEX configuration.
func (dcr *DecredDEX) handleConfigResponse(msg *msgjson.Message) {
	cfg := new(msgjson.ConfigResult)
	err := msg.UnmarshalResult(cfg)
	if err != nil {
		dcr.setWsFail(fmt.Errorf("error unmarshaling config response: %v", err))
		return
	}
	// If the server is not of sufficient version to support the data API,
	// BinSizes will be nil and we won't create any candle caches.
	for _, durStr := range cfg.BinSizes {
		dur, err := time.ParseDuration(durStr)
		if err != nil {
			dcr.setWsFail(fmt.Errorf("unparseable bin size in dcrdex config response: %q: %v", durStr, err))
			return
		}

		var key candlestickKey
		for k, d := range candlestickDurations {
			if d == dur {
				key = k
				break
			}
		}
		if key == "" {
			log.Debugf("Skipping unknown candlestick duration %q", durStr)
			continue
		}

		_, err = dcr.request(msgjson.CandlesRoute, &msgjson.CandlesRequest{
			BaseID:     42,
			QuoteID:    0,
			BinSize:    durStr,
			NumCandles: dexcandles.CacheSize,
		}, func(msg *msgjson.Message) {
			dcr.handleCandles(key, msg)
		})
		if err != nil {
			dcr.setWsFail(fmt.Errorf("error requesting candles for bin size %s: %w", durStr, err))
			return
		}
	}
}

// checkSeq verifies that the seq is sequential, and increments the seq counter.
// checkSeq should only be called with the orderMtx write-locked.
func (dcr *DecredDEX) checkSeq(seq uint64) bool {
	if seq != dcr.seq+1 {
		dcr.setWsFail(fmt.Errorf("incorrect sequence. wanted %d, got %d", dcr.seq+1, seq))
		return false
	}
	dcr.seq = seq
	return true
}

// clearOrderBook clears the order book. clearOrderBook should only be called
// with the orderMtx write-locked.
func (dcr *DecredDEX) clearOrderBook() {
	dcr.buys = make(wsOrders)
	dcr.asks = make(wsOrders)
	dcr.ords = make(map[string]*msgjson.BookOrderNote)
}

// lastStamp is the unix timestamp of the received response or notification.
func (dcr *DecredDEX) lastStamp() int64 {
	dcr.orderMtx.RLock()
	defer dcr.orderMtx.RUnlock()
	return dcr.stamp
}

// setOrderBook processes the order book data from 'orderbook' request.
// setOrderBook should only be called with the orderMtx write-locked.
func (dcr *DecredDEX) setOrderBook(ob *msgjson.OrderBook) {
	dcr.clearOrderBook()
	dcr.seq = ob.Seq
	addToSide := func(side wsOrders, ord *msgjson.BookOrderNote) {
		bucket := side.order(int64(ord.Rate), float64(ord.Rate)/1e8)
		bucket.volume += float64(ord.Quantity) / 1e8
		dcr.ords[ord.OrderID.String()] = ord
	}

	for _, ord := range ob.Orders {
		if ord == nil {
			dcr.setWsFail(fmt.Errorf("nil order encountered"))
			return
		}
		if ord.Side == msgjson.BuyOrderNum {
			addToSide(dcr.buys, ord)
		} else {
			addToSide(dcr.asks, ord)
		}
	}
	dcr.wsInitialized()

	depth := dcr.wsDepthSnapshot()

	dcr.Update(&ExchangeState{
		BaseState: BaseState{
			Price: depth.MidGap(),
			// Change:       priceChange, // With candlesticks
			Stamp: dcr.stamp,
		},
		// Candlesticks: candlesticks, // Not yet
		Depth: depth,
	})
}

// bookOrder processes the 'book_order' notification.
// bookOrder should only be called with the orderMtx write-locked.
func (dcr *DecredDEX) bookOrder(ord *msgjson.BookOrderNote) {
	side := dcr.asks
	if ord.Side == msgjson.BuyOrderNum {
		side = dcr.buys
	}
	bucket := side.order(int64(ord.Rate), float64(ord.Rate)/1e8)
	bucket.volume += float64(ord.Quantity) / 1e8
	dcr.ords[ord.OrderID.String()] = ord
}

// unbookOrder processes the 'unbook_order' notification.
// unbookOrder should only be called with the orderMtx write-locked.
func (dcr *DecredDEX) unbookOrder(note *msgjson.UnbookOrderNote) {
	if len(note.OrderID) == 0 {
		dcr.setWsFail(fmt.Errorf("received unbook_order notification without an order ID"))
		return
	}
	oid := note.OrderID.String()
	ord := dcr.ords[oid]
	if ord == nil {
		dcr.setWsFail(fmt.Errorf("no order found to unbook"))
		return
	}
	delete(dcr.ords, oid)
	side := dcr.asks
	if ord.Side == msgjson.BuyOrderNum {
		side = dcr.buys
	}
	rateKey := int64(ord.Rate)
	bucket := side.order(rateKey, float64(ord.Rate)/1e8)
	bucket.volume -= float64(ord.Quantity) / 1e8
	if bucket.volume < 1e-8 { // Account for floating point imprecision.
		delete(side, rateKey)
	}
}

// updateRemaining processes the 'update_remaining' notification.
// updateRemaining should only be called with the orderMtx write-locked.
func (dcr *DecredDEX) updateRemaining(update *msgjson.UpdateRemainingNote) {
	if len(update.OrderID) == 0 {
		dcr.setWsFail(fmt.Errorf("received update_remaining notification without an order ID"))
		return
	}
	oid := update.OrderID.String()
	ord := dcr.ords[oid]
	if ord == nil {
		dcr.setWsFail(fmt.Errorf("order %s from dex.decred.org was not in our book", oid))
		return
	}

	diff := ord.Quantity - update.Remaining
	ord.Quantity = update.Remaining
	side := dcr.asks
	if ord.Side == msgjson.BuyOrderNum {
		side = dcr.buys
	}
	rateKey := int64(ord.Rate)
	bucket := side.order(rateKey, float64(ord.Rate)/1e8)
	bucket.volume -= float64(diff) / 1e8
	if bucket.volume < 1e-8 {
		delete(side, rateKey)
	}
}
