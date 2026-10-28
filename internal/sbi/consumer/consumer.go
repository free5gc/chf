package consumer

import (
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/pkg/app"
	Nnrf_NFDiscovery "github.com/free5gc/openapi/nrf/NFDisc"
	Nnrf_NFManagement "github.com/free5gc/openapi/nrf/NFMgmt"
	"github.com/free5gc/util/nfheartbeat"
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
	heartbeat, err := nfheartbeat.NewRunner(
		nrfRegistrar{c.nnrfService},
		func() int32 { return c.Config().GetNfHeartBeatTimer() },
		logger.ConsumerLog,
	)
	if err != nil {
		return nil, err
	}
	c.heartbeat = heartbeat

	return c, nil
}
