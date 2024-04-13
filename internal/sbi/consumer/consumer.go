package consumer

import (
	"context"

	"github.com/free5gc/chf/pkg/factory"
	"github.com/free5gc/openapi/Nnrf_NFDiscovery"
	"github.com/free5gc/openapi/Nnrf_NFManagement"

	chf_context "github.com/free5gc/chf/internal/context"
)

type chf interface {
	Config() *factory.Config
	Context() *chf_context.CHFContext
	CancelContext() context.Context
}

type Consumer struct {
	chf

	// consumer services
	*nnrfService
}

func NewConsumer(chf chf) (*Consumer, error) {
	c := &Consumer{
		chf: chf,
	}

	c.nnrfService = &nnrfService{
		consumer:        c,
		nfMngmntClients: make(map[string]*Nnrf_NFManagement.APIClient),
		nfDiscClients:   make(map[string]*Nnrf_NFDiscovery.APIClient),
	}
	return c, nil
}
