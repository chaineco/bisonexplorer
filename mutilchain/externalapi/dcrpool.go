package externalapi

import (
	"net/http"
	"sort"

	"github.com/decred/dcrdata/v8/db/dbtypes"
)

const (
	THREEPOOL   = "threepool"
	MININGANDCO = "miningandco"
	E4POOL      = "e4pool"
)

var threePoolURL = `https://dcr.threepool.tech/blocks`
var e4poolURL = `https://dcr.e4pool.com/blocks`
var miningandcoURL = `https://decred.miningandco.com/blocks`

type PoolResponse struct {
	Data []*dbtypes.PoolDataItem `json:"data"`
}

func GetPoolData(poolURL, poolType string) ([]*dbtypes.PoolDataItem, error) {
	query := map[string]string{
		"pageSize":   "5",
		"pageNumber": "1",
	}
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: poolURL,
		Payload: query,
	}
	var responseData PoolResponse
	if err := SkipTLSHttpRequest(req, &responseData); err != nil {
		return nil, err
	}
	for index, data := range responseData.Data {
		data.PoolType = poolType
		responseData.Data[index] = data
	}
	return responseData.Data, nil
}

func GetLastBlocksPool() ([]*dbtypes.PoolDataItem, error) {
	log.Debugf("Start handler get pool info API")
	// get 5 pool blocks from threepool
	threepoolRes, err := GetPoolData(threePoolURL, THREEPOOL)
	if err != nil {
		threepoolRes = make([]*dbtypes.PoolDataItem, 0)
	}
	// get 5 pool blocks from e4pool
	e4poolRes, err := GetPoolData(e4poolURL, E4POOL)
	if err == nil {
		threepoolRes = append(threepoolRes, e4poolRes...)
	}
	// get 5 pool blocks from miningandco
	miningandcoRes, err := GetPoolData(miningandcoURL, MININGANDCO)
	if err == nil {
		threepoolRes = append(threepoolRes, miningandcoRes...)
	}
	sort.Slice(threepoolRes, func(i, j int) bool {
		return threepoolRes[i].BlockHeight > threepoolRes[j].BlockHeight
	})
	if len(threepoolRes) < 5 {
		return threepoolRes, nil
	}
	log.Debugf("Finished handler get pool info API")
	return threepoolRes[:5], nil
}
