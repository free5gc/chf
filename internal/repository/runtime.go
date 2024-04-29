package repository

import (
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/pkg/factory"
)

type RuntimeRepository struct {
	config *factory.Config
	chfCtx *chf_context.CHFContext
}

func NewRuntimeRepository(cfg *factory.Config) *RuntimeRepository {
	return &RuntimeRepository{
		config: cfg,
		chfCtx: chf_context.GetSelf(),
	}
}

func (rr RuntimeRepository) Config() *factory.Config {
	return rr.config
}

func (rr RuntimeRepository) Context() *chf_context.CHFContext {
	return rr.chfCtx
}
