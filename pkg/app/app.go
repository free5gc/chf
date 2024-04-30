package app

import (
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/pkg/factory"
)

type App interface {
	SetLogEnable(enable bool)
	SetLogLevel(level string)
	SetReportCaller(reportCaller bool)

	// tlsKeyLogPath would be remove
	Start(tlsKeyLogPath string)
	Terminate()

	Context() *chf_context.CHFContext
	Config() *factory.Config
}
