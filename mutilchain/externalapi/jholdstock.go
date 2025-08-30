package externalapi

import (
	"net/http"
)

var worldNodesUrl = `https://nodes.jholdstock.uk/world_nodes`

func GetWorldNodesCount() (int, error) {
	log.Debugf("Start get world nodes number from jholdstock.uk")
	var responseArr []interface{}
	req := &ReqConfig{
		Method:  http.MethodGet,
		HttpUrl: worldNodesUrl,
		Payload: map[string]string{},
	}
	err := HttpRequest(req, &responseArr)
	if err != nil {
		log.Debugf("Error: Get world nodes number from jholdstock.uk failed: %v", err)
		return 0, err
	}
	log.Debugf("Finish get world nodes number from jholdstock.uk")
	return len(responseArr), nil
}
