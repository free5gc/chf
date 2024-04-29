package consumer

import (
	"github.com/free5gc/chf/internal/repository"

	"github.com/free5gc/openapi/Nnrf_NFDiscovery"
	"github.com/free5gc/openapi/Nnrf_NFManagement"
)

type Chf interface {
	// Consumer doesn't need any App component now
}

type Consumer struct {
	Chf
	RuntimeRepository *repository.RuntimeRepository

	*nnrfService
}

func NewConsumer(chf Chf, runtimeRepo *repository.RuntimeRepository) (*Consumer, error) {
	c := &Consumer{
		Chf:               chf,
		RuntimeRepository: runtimeRepo,
	}

	c.nnrfService = &nnrfService{
		consumer:        c,
		nfMngmntClients: make(map[string]*Nnrf_NFManagement.APIClient),
		nfDiscClients:   make(map[string]*Nnrf_NFDiscovery.APIClient),
	}
	return c, nil
}
