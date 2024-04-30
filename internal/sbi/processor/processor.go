package processor

import (
	"github.com/free5gc/chf/internal/repository"
)

type Chf interface {
	// Processor doesn't need any App component now
}

type Processor struct {
	Chf
	RuntimeRepository *repository.RuntimeRepository
}

type HandlerResponse struct {
	Status  int
	Headers map[string][]string
	Body    interface{}
}

func NewProcessor(chf Chf, runtimeRepo *repository.RuntimeRepository) (*Processor, error) {
	p := &Processor{
		Chf:               chf,
		RuntimeRepository: runtimeRepo,
	}
	return p, nil
}
