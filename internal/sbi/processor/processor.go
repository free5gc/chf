package processor

import (
	"context"
	"fmt"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
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
	journal      charging.Journal
	accountDebit accountDebitFunc
	bindAccount  func(context.Context, *charging_datatype.AccountDebitRequest) ([]byte, error)
	serviceUsage serviceUsageFunc
}

type HandlerResponse struct {
	Status  int
	Headers map[string][]string
	Body    interface{}
}

func NewProcessor(chf ProcessorChf, journal charging.Journal, accounts *charging.Accounts) (*Processor, error) {
	if accounts == nil {
		return nil, fmt.Errorf("account binding store is unavailable")
	}
	p := &Processor{
		ProcessorChf: chf,
		journal:      journal,
		bindAccount:  accounts.Bind,
	}
	return p, nil
}
