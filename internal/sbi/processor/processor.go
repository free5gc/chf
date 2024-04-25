package processor

import (
	"context"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/sbi/consumer"
	"github.com/free5gc/chf/pkg/factory"
)

type Chf interface {
	Config() *factory.Config
	Context() *chf_context.CHFContext
	Consumer() *consumer.Consumer
	CancelContext() context.Context
}

type Processor struct {
	Chf
}

type HandlerResponse struct {
	Status  int
	Headers map[string][]string
	Body    interface{}
}

func NewProcessor(chf Chf) (*Processor, error) {
	p := &Processor{
		Chf: chf,
	}
	return p, nil
}
