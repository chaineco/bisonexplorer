package externalapi

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/decred/dcrdata/v8/db/cache"
)

var blockdayUrl = `https://explorer.coinex.com/res/%s/statis/blockday`
var mempoolUrl = `https://explorer.coinex.com/res/%s/statis/mempooltime`

type ResponseData struct {
	Code int           `json:"code"`
	Data []interface{} `json:"data"`
}

type AddressData struct {
	Time       string `json:"time"`
	AddressNum string `json:"addr_num"`
}

type DifficultyData struct {
	Time       string `json:"time"`
	Difficulty string `json:"difficulty"`
}

type HashrateData struct {
	Time     string `json:"time"`
	Hashrate string `json:"hashrate"`
}

type CoinSupplyData struct {
	Time             string `json:"time"`
	TotalBlockReward string `json:"total_block_reward"`
}

type TxFeeAvgData struct {
	Time   string `json:"time"`
	FeeAvg string `json:"fee_avg"`
}

type MempoolSizeData struct {
	Time  string `json:"time"`
	Bytes string `json:"bytes"`
}

type MempoolTxCountData struct {
	Time string `json:"time"`
	Size string `json:"size"`
}

type TxTotalData struct {
	Time       string `json:"time"`
	TotalTxNum string `json:"total_txnum"`
}

type NewMinedBlocksData struct {
	Time      string `json:"time"`
	BlockNum  string `json:"block_num"`
	BlockSize string `json:"block_size"`
}

type BlockchainSizeData struct {
	Time           string `json:"time"`
	TotalBlockSize string `json:"total_block_size"`
}

type BlockSizeData struct {
	Time         string `json:"time"`
	BlockAvgSize string `json:"block_avg_size"`
}

type TxNumPerBlockAvgData struct {
	Time          string `json:"time"`
	BlockAvgTxNum string `json:"block_avg_txnum"`
}

func HandlerMutilchainChartsData(charts *cache.MutilchainChartData) error {
	//handler for block chain size chart
	HandlerBlockchainSizeData(charts)
	//hanlder for blocksize
	HandlerBlockSizeData(charts)
	//handler for number per block average
	HandlerTxNumPerBlockAvg(charts)
	//handler for mined blocks
	HandlerMinedBlocks(charts)
	//handler for total tx number
	HandlerTxTotalNum(charts)
	//handler for mempool tx count
	HanlderMempoolTxCount(charts)
	//handler for mempool size
	HandlerMempoolSize(charts)
	//handler for tx fee avg
	HandlerTxFeeAvg(charts)
	//handler for coin supply
	HandlerCoinSupply(charts)
	//handler for hashrate
	HanlderHashrate(charts)
	//handler for difficutly
	HanlderDifficulty(charts)
	//handler for address number
	HanlderAddressNumber(charts)
	return nil
}

func HanlderAddressNumber(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on Address number API for %s", charts.ChainType)
	url := fmt.Sprintf(blockdayUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "addr_num",
		"period":    "ALL",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:            newChartUints(),
		APIAddressCount: newChartUints(),
	}
	for _, addrCountInterface := range responseData.Data {
		var addrCountData AddressData
		parseDataErr := ConvertInterfaceToStruct(addrCountInterface, &addrCountData)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(addrCountData.Time)
		addrCount, addrErr := ConvertStringToInt(addrCountData.AddressNum)
		if timeErr != nil || addrErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.APIAddressCount = append(zoomSet.APIAddressCount, uint64(addrCount))
	}
	charts.APIAddressCount = zoomSet
	log.Debugf("Finish handler on Address number API for %s", charts.ChainType)
	return nil
}

func HanlderDifficulty(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on difficulty API for %s", charts.ChainType)
	url := fmt.Sprintf(blockdayUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "difficulty",
		"period":    "ALL",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:       newChartUints(),
		Difficulty: newChartFloats(),
	}
	for _, diffcutlyInterface := range responseData.Data {
		var difficultyData DifficultyData
		parseDataErr := ConvertInterfaceToStruct(diffcutlyInterface, &difficultyData)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(difficultyData.Time)
		diffNumber, diffErr := ConvertStringToFloat(difficultyData.Difficulty)
		if timeErr != nil || diffErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.Difficulty = append(zoomSet.Difficulty, diffNumber)
	}
	charts.APIDifficulty = zoomSet
	log.Debugf("Finish handler on difficulty API for %s", charts.ChainType)
	return nil
}

func HanlderHashrate(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on hashrate API for %s", charts.ChainType)
	url := fmt.Sprintf(blockdayUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "hashrate",
		"period":    "ALL",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:     newChartUints(),
		Hashrate: newChartFloats(),
	}
	for _, hashrateInterface := range responseData.Data {
		var hashrateData HashrateData
		parseDataErr := ConvertInterfaceToStruct(hashrateInterface, &hashrateData)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(hashrateData.Time)
		hashrateNumber, hashrateErr := ConvertStringToFloat(hashrateData.Hashrate)
		if timeErr != nil || hashrateErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.Hashrate = append(zoomSet.Hashrate, hashrateNumber)
	}
	charts.APIHashrate = zoomSet
	log.Debugf("Finish handler on hashrate API for %s", charts.ChainType)
	return nil
}

func HandlerCoinSupply(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on coin suplly API for %s", charts.ChainType)
	url := fmt.Sprintf(blockdayUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "total_block_reward",
		"period":    "ALL",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:     newChartUints(),
		NewAtoms: newChartUints(),
	}
	for _, coinSupplyInterface := range responseData.Data {
		var coinSupply CoinSupplyData
		parseDataErr := ConvertInterfaceToStruct(coinSupplyInterface, &coinSupply)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(coinSupply.Time)
		coinSupplyNumber, coinSupplyErr := ConvertStringToFloat(coinSupply.TotalBlockReward)
		if timeErr != nil || coinSupplyErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.NewAtoms = append(zoomSet.NewAtoms, uint64(math.Floor(coinSupplyNumber)))
	}
	charts.APICoinSupply = zoomSet
	log.Debugf("Finish handler on coin suplly API for %s", charts.ChainType)
	return nil
}

func HandlerTxFeeAvg(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on tx fee average API for %s", charts.ChainType)
	url := fmt.Sprintf(blockdayUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "fee_avg",
		"period":    "ALL",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time: newChartUints(),
		Fees: newChartUints(),
	}
	for _, txFeeAvgInterface := range responseData.Data {
		var txFeeAvg TxFeeAvgData
		parseDataErr := ConvertInterfaceToStruct(txFeeAvgInterface, &txFeeAvg)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(txFeeAvg.Time)
		txFeeAvgNum, txFeeAvgErr := ConvertStringToFloat(txFeeAvg.FeeAvg)
		if timeErr != nil || txFeeAvgErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.Fees = append(zoomSet.Fees, uint64(math.Floor(txFeeAvgNum*1e8)))
	}
	charts.APITxFeeAvg = zoomSet
	log.Debugf("Finish handler on tx fee average API for %s", charts.ChainType)
	return nil
}

func HandlerMempoolSize(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on mempool size API for %s", charts.ChainType)
	url := fmt.Sprintf(mempoolUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "bytes",
		"period":    "undefined",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:           newChartUints(),
		APIMempoolSize: newChartUints(),
	}
	for _, memSizeInterface := range responseData.Data {
		var memSize MempoolSizeData
		parseDataErr := ConvertInterfaceToStruct(memSizeInterface, &memSize)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(memSize.Time)
		memKbSize, memErr := ConvertStringToFloat(memSize.Bytes)
		if timeErr != nil || memErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.APIMempoolSize = append(zoomSet.APIMempoolSize, uint64(math.Floor(memKbSize*math.Pow(2, 10))))
	}
	charts.APIMempoolSize = zoomSet
	log.Debugf("Finish handler on mempool size API for %s", charts.ChainType)
	return nil
}

func HanlderMempoolTxCount(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on mempool tx count API for %s", charts.ChainType)
	url := fmt.Sprintf(mempoolUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "size",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:            newChartUints(),
		APIMempoolTxNum: newChartUints(),
	}
	for _, mempoolTxInterface := range responseData.Data {
		var memTxCount MempoolTxCountData
		parseDataErr := ConvertInterfaceToStruct(mempoolTxInterface, &memTxCount)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(memTxCount.Time)
		txTotalNum, txTotalErr := ConvertStringToInt(memTxCount.Size)
		if timeErr != nil || txTotalErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.APIMempoolTxNum = append(zoomSet.APIMempoolTxNum, uint64(txTotalNum))
	}
	charts.APIMempoolTxCount = zoomSet
	log.Debugf("Finish handler on mempool tx count API for %s", charts.ChainType)
	return nil
}

func HandlerTxTotalNum(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on tx total count API for %s", charts.ChainType)
	url := fmt.Sprintf(blockdayUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "total_txnum",
		"period":    "ALL",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:    newChartUints(),
		TxCount: newChartUints(),
	}
	for _, txTotalNumInterface := range responseData.Data {
		var txNumData TxTotalData
		parseDataErr := ConvertInterfaceToStruct(txTotalNumInterface, &txNumData)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(txNumData.Time)
		txTotalNum, txTotalErr := ConvertStringToInt(txNumData.TotalTxNum)
		if timeErr != nil || txTotalErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.TxCount = append(zoomSet.TxCount, uint64(txTotalNum))
	}
	charts.APITxTotal = zoomSet
	log.Debugf("Finish handler on tx total count API for %s", charts.ChainType)
	return nil
}

func HandlerMinedBlocks(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on mined blocks API for %s", charts.ChainType)
	url := fmt.Sprintf(blockdayUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "block_size,block_num",
		"period":    "ALL",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:           newChartUints(),
		APIMinedBlocks: newChartUints(),
		APIMinedSize:   newChartUints(),
	}
	for _, minedInterface := range responseData.Data {
		var minedBlocksData NewMinedBlocksData
		parseDataErr := ConvertInterfaceToStruct(minedInterface, &minedBlocksData)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(minedBlocksData.Time)
		blNum, blNumErr := ConvertStringToInt(minedBlocksData.BlockNum)
		blSize, blSizeErr := ConvertStringToFloat(minedBlocksData.BlockSize)
		if timeErr != nil || blNumErr != nil || blSizeErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.APIMinedBlocks = append(zoomSet.APIMinedBlocks, uint64(blNum))
		zoomSet.APIMinedSize = append(zoomSet.APIMinedSize, uint64(math.Floor(blSize*math.Pow(2, 20))))
	}
	charts.APINewMinedBlocks = zoomSet
	log.Debugf("Finish handler on mined blocks API for %s", charts.ChainType)
	return nil
}

func HandlerTxNumPerBlockAvg(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on tx number per block average API for %s", charts.ChainType)
	url := fmt.Sprintf(blockdayUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "block_avg_txnum",
		"period":    "ALL",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:         newChartUints(),
		APITxAverage: newChartUints(),
	}
	for _, blockTxNumInterface := range responseData.Data {
		var blTxNumData TxNumPerBlockAvgData
		parseDataErr := ConvertInterfaceToStruct(blockTxNumInterface, &blTxNumData)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(blTxNumData.Time)
		txNumAvg, blAvgErr := ConvertStringToFloat(blTxNumData.BlockAvgTxNum)
		if timeErr != nil || blAvgErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.APITxAverage = append(zoomSet.APITxAverage, uint64(math.Floor(txNumAvg)))
	}
	charts.APITxNumPerBlockAvg = zoomSet
	log.Debugf("Finish handler on tx number per block average API for %s", charts.ChainType)
	return nil
}

func HandlerBlockSizeData(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on blockchain size API for %s", charts.ChainType)
	url := fmt.Sprintf(blockdayUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "block_avg_size",
		"period":    "ALL",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:      newChartUints(),
		BlockSize: newChartUints(),
	}
	for _, blSizeInterface := range responseData.Data {
		var blSize BlockSizeData
		parseDataErr := ConvertInterfaceToStruct(blSizeInterface, &blSize)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(blSize.Time)
		blMBSize, blErr := ConvertStringToFloat(blSize.BlockAvgSize)
		if timeErr != nil || blErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.BlockSize = append(zoomSet.BlockSize, uint64(math.Floor(blMBSize*math.Pow(2, 20))))
	}
	charts.APIBlockSize = zoomSet
	log.Debugf("Finish handler on blockchain size API for %s", charts.ChainType)
	return nil
}

func HandlerBlockchainSizeData(charts *cache.MutilchainChartData) error {
	log.Debugf("Start handler on blockchain size API for %s", charts.ChainType)
	url := fmt.Sprintf(blockdayUrl, charts.ChainType)
	query := map[string]string{
		"reqfields": "total_block_size",
		"period":    "ALL",
	}
	var responseData ResponseData
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: url,
		Payload: query,
	}
	if err := HttpRequest(req, &responseData); err != nil {
		return err
	}
	zoomSet := &cache.ZoomSet{
		Time:              newChartUints(),
		APIBlockchainSize: newChartUints(),
	}
	for _, blSizeInterface := range responseData.Data {
		var blChainSize BlockchainSizeData
		parseDataErr := ConvertInterfaceToStruct(blSizeInterface, &blChainSize)
		if parseDataErr != nil {
			continue
		}
		time, timeErr := ConvertStringToInt(blChainSize.Time)
		blMBSize, blErr := ConvertStringToFloat(blChainSize.TotalBlockSize)
		if timeErr != nil || blErr != nil {
			continue
		}
		zoomSet.Time = append(zoomSet.Time, uint64(time))
		zoomSet.APIBlockchainSize = append(zoomSet.APIBlockchainSize, uint64(math.Floor(blMBSize*math.Pow(2, 20))))
	}
	charts.APIBlockchainSize = zoomSet
	log.Debugf("Finish handler on blockchain size API for %s", charts.ChainType)
	return nil
}

func ConvertStringToInt(numString string) (int64, error) {
	return strconv.ParseInt(numString, 0, 32)
}

func ConvertStringToFloat(numString string) (float64, error) {
	return strconv.ParseFloat(numString, 64)
}

func newChartUints() cache.ChartUints {
	return make(cache.ChartUints, 0)
}

func newChartFloats() cache.ChartFloats {
	return make(cache.ChartFloats, 0)
}

func ConvertInterfaceToStruct(input interface{}, parseObj interface{}) error {
	jsonBytes, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return json.Unmarshal(jsonBytes, &parseObj)
}
