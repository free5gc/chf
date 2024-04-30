package processor

type ProcessorChf interface {
	// Processor doesn't need any App component now
}

type Processor struct {
	ProcessorChf
}

type HandlerResponse struct {
	Status  int
	Headers map[string][]string
	Body    interface{}
}

func NewProcessor(chf ProcessorChf) (*Processor, error) {
	p := &Processor{
		ProcessorChf: chf,
	}
	return p, nil
}
