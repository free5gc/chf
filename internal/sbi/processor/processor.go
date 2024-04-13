package processor

import (
	"context"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/sbi/consumer"
	"github.com/free5gc/chf/pkg/factory"
)

type chf interface {
	Config() *factory.Config
	Context() *chf_context.CHFContext
	Consumer() *consumer.Consumer
	CancelContext() context.Context
}

type Processor struct {
	chf
}

type HandlerResponse struct {
	Status  int
	Headers map[string][]string
	Body    interface{}
}

func NewProcessor(chf chf) (*Processor, error) {
	p := &Processor{
		chf: chf,
	}
	return p, nil
}
