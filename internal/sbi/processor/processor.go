package processor

import (
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

func NewProcessor(chf ProcessorChf) (*Processor, error) {
	return &Processor{ProcessorChf: chf}, nil
}
