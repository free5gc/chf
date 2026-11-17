package processor

import (
	"fmt"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/pkg/app"
)

type ProcessorChf interface {
	app.App
}

type accountDebitFunc func(
	*chf_context.ChfUe, *charging_datatype.AccountDebitRequest,
) (*charging_datatype.AccountDebitResponse, error)

type serviceUsageFunc func(
	*chf_context.ChfUe, *charging_datatype.ServiceUsageRequest,
) (*charging_datatype.ServiceUsageResponse, error)

type Processor struct {
	ProcessorChf
	requests     charging.RequestCache
	accountDebit accountDebitFunc
	serviceUsage serviceUsageFunc
	cdrWriter    func(string, []*cdrType.CHFRecord) error
}

type HandlerResponse struct {
	Status  int
	Headers map[string][]string
	Body    interface{}
}

// Stop cancels invocation timers and waits for resource cleanup already running.
func (p *Processor) Stop() {
	p.requests.Stop()
}

func NewProcessor(chf ProcessorChf) (*Processor, error) {
	retention := chf.Config().Configuration.ChargingRetryRetention
	if retention == 0 {
		retention = charging.UncertainTimeout
	}
	// Keep CHF's retention below the ABMF replay lifetime.
	if retention <= 0 || retention >= charging.AccountReplayWindow {
		return nil, fmt.Errorf("chargingRetryRetention must be positive and less than %s", charging.AccountReplayWindow)
	}
	p := &Processor{ProcessorChf: chf}
	p.requests.RetryRetention = retention
	return p, nil
}
