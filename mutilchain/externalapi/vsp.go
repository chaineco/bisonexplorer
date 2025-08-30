package externalapi

import (
	"net/http"
)

var vspURL = `https://api.decred.org`

type VSPResponse struct {
	Network                    string  `json:"network"`
	LastUpdated                int64   `json:"lastupdated"`
	FeePercentage              float64 `json:"feepercentage"`
	Voting                     int64   `json:"voting"`
	Voted                      int64   `json:"voted"`
	Missed                     int64   `json:"missed"`
	Expired                    int64   `json:"expired"`
	VspdVersion                string  `json:"vspdversion"`
	EstimatedNetworkProportion float64 `json:"estimatednetworkproportion"`
	VSPLink                    string  `json:"vspLink"`
}

func GetVSPList() ([]VSPResponse, error) {
	log.Debugf("Start get vsp list from API")
	query := map[string]string{
		"c": "vsp",
	}
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: vspURL,
		Payload: query,
	}
	var responseData map[string]VSPResponse
	if err := HttpRequest(req, &responseData); err != nil {
		return nil, err
	}
	result := make([]VSPResponse, 0)
	for key, value := range responseData {
		if value.Network == "testnet" {
			continue
		}
		value.VSPLink = key
		result = append(result, value)
	}
	log.Debugf("Finished get vsp list from API")
	return result, nil
}
