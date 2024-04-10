package service

import (
	"context"
	"io"
	"os"
	"runtime/debug"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/free5gc/chf/internal/cgf"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/sbi"
	"github.com/free5gc/chf/internal/sbi/consumer"
	"github.com/free5gc/chf/pkg/abmf"
	"github.com/free5gc/chf/pkg/factory"
	"github.com/free5gc/chf/pkg/rf"
)

type ChfApp struct {
	cfg    *factory.Config
	chfCtx *chf_context.CHFContext
	ctx    context.Context
	cancel context.CancelFunc

	sbiServer *sbi.Server
	wg        sync.WaitGroup
}

func NewApp(ctx context.Context, cfg *factory.Config, tlsKeyLogPath string) (*ChfApp, error) {
	chf := &ChfApp{
		cfg: cfg,
		wg:  sync.WaitGroup{},
	}
	chf.SetLogEnable(cfg.GetLogEnable())
	chf.SetLogLevel(cfg.GetLogLevel())
	chf.SetReportCaller(cfg.GetLogReportCaller())

	chf_context.Init()
	chf.chfCtx = chf_context.GetSelf()

	chf.ctx, chf.cancel = context.WithCancel(ctx)

	var err error
	if chf.sbiServer, err = sbi.NewServer(chf, tlsKeyLogPath); err != nil {
		return nil, err
	}
	return chf, nil
}

func (a *ChfApp) Config() *factory.Config {
	return a.cfg
}

func (a *ChfApp) Context() *chf_context.CHFContext {
	return a.chfCtx
}

func (a *ChfApp) CancelContext() context.Context {
	return a.ctx
}

func (c *ChfApp) SetLogEnable(enable bool) {
	logger.MainLog.Infof("Log enable is set to [%v]", enable)
	if enable && logger.Log.Out == os.Stderr {
		return
	} else if !enable && logger.Log.Out == io.Discard {
		return
	}

	c.cfg.SetLogEnable(enable)
	if enable {
		logger.Log.SetOutput(os.Stderr)
	} else {
		logger.Log.SetOutput(io.Discard)

	}
}

func (c *ChfApp) SetLogLevel(level string) {
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		logger.MainLog.Warnf("Log level [%s] is invalid", level)
		return
	}

	logger.MainLog.Infof("Log level is set to [%s]", level)
	if lvl == logger.Log.GetLevel() {
		return
	}

	c.cfg.SetLogLevel(level)
	logger.Log.SetLevel(lvl)
}

func (a *ChfApp) SetReportCaller(reportCaller bool) {
	logger.MainLog.Infof("Report Caller is set to [%v]", reportCaller)
	if reportCaller == logger.Log.ReportCaller {
		return
	}

	a.cfg.SetLogReportCaller(reportCaller)
	logger.Log.SetReportCaller(reportCaller)
}

func (a *ChfApp) Start(tlsKeyLogPath string) {
	logger.InitLog.Infoln("Server started")

	// router := logger_util.NewGinWithLogrus(logger.GinLog)
	// convergedcharging.AddService(router)

	a.wg.Add(1)
	cgf.OpenServer(a.ctx, &a.wg)

	a.wg.Add(1)
	rf.OpenServer(a.ctx, &a.wg)

	a.wg.Add(1)
	abmf.OpenServer(a.ctx, &a.wg)

	self := a.chfCtx

	// Register to NRF
	profile, err := consumer.BuildNFInstance(self)
	if err != nil {
		logger.InitLog.Error("Build CHF Profile Error")
	}
	_, self.NfId, err = consumer.SendRegisterNFInstance(self.NrfUri, self.NfId, profile)
	if err != nil {
		logger.InitLog.Errorf("CHF register to NRF Error[%s]", err.Error())
	}

	a.wg.Add(1)
	go a.listenShutdownEvent()

	if err := a.sbiServer.Run(context.Background(), &a.wg); err != nil {
		logger.InitLog.Fatalf("Run SBI server failed: %+v", err)
	}

	// signalChannel := make(chan os.Signal, 1)
	// signal.Notify(signalChannel, os.Interrupt, syscall.SIGTERM)
	// <-signalChannel

	// a.cancel()
	// a.Terminate()
	// a.wg.Wait()

	// os.Exit(0)
}

func (a *ChfApp) listenShutdownEvent() {
	defer func() {
		if p := recover(); p != nil {
			// Print stack for panic to log. Fatalf() will let program exit.
			logger.InitLog.Fatalf("panic: %v\n%s", p, string(debug.Stack()))
		}
		a.wg.Done()
	}()

	<-a.ctx.Done()

	if a.sbiServer != nil {
		a.Terminate()
		a.sbiServer.Stop(context.Background())
	}
}

func (c *ChfApp) Terminate() {
	logger.InitLog.Infof("Terminating CHF...")
	// deregister with NRF
	problemDetails, err := consumer.SendDeregisterNFInstance()
	if problemDetails != nil {
		logger.InitLog.Errorf("Deregister NF instance Failed Problem[%+v]", problemDetails)
	} else if err != nil {
		logger.InitLog.Errorf("Deregister NF instance Error[%+v]", err)
	} else {
		logger.InitLog.Infof("Deregister from NRF successfully")
	}
	logger.InitLog.Infof("CHF terminated")
}

func (a *ChfApp) Stop() {
	a.cancel()
	// a.WaitRoutineStopped()
}

func (a *ChfApp) WaitRoutineStopped() {
	a.wg.Wait()
	logger.MainLog.Infof("NRF App is terminated")
}
