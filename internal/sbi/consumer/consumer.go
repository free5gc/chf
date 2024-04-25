package consumer

import (
	"context"

	"github.com/free5gc/chf/pkg/factory"
	"github.com/free5gc/openapi/Nnrf_NFDiscovery"
	"github.com/free5gc/openapi/Nnrf_NFManagement"

	chf_context "github.com/free5gc/chf/internal/context"
)

type Chf interface {
	Config() *factory.Config
	Context() *chf_context.CHFContext
	CancelContext() context.Context
}

type Consumer struct {
	Chf

	*nnrfService
}

func NewConsumer(chf Chf) (*Consumer, error) {
	c := &Consumer{
		Chf: chf,
	}

	c.nnrfService = &nnrfService{
		consumer:        c,
		nfMngmntClients: make(map[string]*Nnrf_NFManagement.APIClient),
		nfDiscClients:   make(map[string]*Nnrf_NFDiscovery.APIClient),
	}
	return c, nil
}
