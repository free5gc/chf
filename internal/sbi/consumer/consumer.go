package consumer

import (
	"github.com/free5gc/chf/pkg/app"

	// "github.com/free5gc/openapi/Nnrf_NFDiscovery"
	Nnrf_NFDiscovery "github.com/free5gc/openapi-r17/nrf/NFDiscovery"
	// "github.com/free5gc/openapi/Nnrf_NFManagement"
	Nnrf_NFManagement "github.com/free5gc/openapi-r17/nrf/NFManagement"
)

type ConsumerChf interface {
	app.App
}

type Consumer struct {
	ConsumerChf

	*nnrfService
}

func NewConsumer(chf ConsumerChf) (*Consumer, error) {
	c := &Consumer{
		ConsumerChf: chf,
	}

	c.nnrfService = &nnrfService{
		consumer:        c,
		nfMngmntClients: make(map[string]*Nnrf_NFManagement.APIClient),
		nfDiscClients:   make(map[string]*Nnrf_NFDiscovery.APIClient),
	}
	return c, nil
}
