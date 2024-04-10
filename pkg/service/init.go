package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"

	"github.com/gin-contrib/cors"
	"github.com/sirupsen/logrus"

	"github.com/free5gc/chf/internal/cgf"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/sbi/consumer"
	"github.com/free5gc/chf/internal/sbi/convergedcharging"
	"github.com/free5gc/chf/pkg/abmf"
	"github.com/free5gc/chf/pkg/factory"
	"github.com/free5gc/chf/pkg/rf"
	"github.com/free5gc/util/httpwrapper"
	logger_util "github.com/free5gc/util/logger"
)

type ChfApp struct {
	cfg    *factory.Config
	chfCtx *chf_context.CHFContext
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewApp(ctx context.Context, cfg *factory.Config) (*ChfApp, error) {
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

	return chf, nil
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

func (c *ChfApp) Start(tlsKeyLogPath string) {
	logger.InitLog.Infoln("Server started")
	router := logger_util.NewGinWithLogrus(logger.GinLog)

	convergedcharging.AddService(router)

	router.Use(cors.New(cors.Config{
		AllowMethods: []string{"GET", "POST", "OPTIONS", "PUT", "PATCH", "DELETE"},
		AllowHeaders: []string{
			"Origin", "Content-Length", "Content-Type", "User-Agent",
			"Referrer", "Host", "Token", "X-Requested-With",
		},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		AllowAllOrigins:  true,
		MaxAge:           86400,
	}))

	pemPath := factory.ChfDefaultTLSPemPath
	keyPath := factory.ChfDefaultTLSKeyPath
	sbi := factory.ChfConfig.Configuration.Sbi
	if sbi.Tls != nil {
		pemPath = sbi.Tls.Pem
		keyPath = sbi.Tls.Key
	}

	self := c.chfCtx

	wg := sync.WaitGroup{}

	ctx, cancel := context.WithCancel(context.Background())

	wg.Add(1)
	cgf.OpenServer(ctx, &wg)

	wg.Add(1)
	rf.OpenServer(ctx, &wg)

	wg.Add(1)
	abmf.OpenServer(ctx, &wg)
	// Register to NRF
	profile, err := consumer.BuildNFInstance(self)
	if err != nil {
		logger.InitLog.Error("Build CHF Profile Error")
	}
	_, self.NfId, err = consumer.SendRegisterNFInstance(self.NrfUri, self.NfId, profile)
	if err != nil {
		logger.InitLog.Errorf("CHF register to NRF Error[%s]", err.Error())
	}

	addr := fmt.Sprintf("%s:%d", self.BindingIPv4, self.SBIPort)

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				// Print stack for panic to log. Fatalf() will let program exit.
				logger.InitLog.Fatalf("panic: %v\n%s", p, string(debug.Stack()))
			}
		}()

		<-signalChannel
		cancel()
		c.Terminate()
		wg.Wait()
		os.Exit(0)
	}()

	server, err := httpwrapper.NewHttp2Server(addr, tlsKeyLogPath, router)
	if server == nil {
		logger.InitLog.Errorf("Initialize HTTP server failed: %+v", err)
		return
	}

	if err != nil {
		logger.InitLog.Warnf("Initialize HTTP server: +%v", err)
	}

	serverScheme := factory.ChfConfig.Configuration.Sbi.Scheme
	if serverScheme == "http" {
		err = server.ListenAndServe()
	} else if serverScheme == "https" {
		err = server.ListenAndServeTLS(pemPath, keyPath)
	}

	if err != nil {
		logger.InitLog.Fatalf("HTTP server setup failed: %+v", err)
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

func (a *ChfApp) WaitRoutineStopped() {
	a.wg.Wait()
	logger.MainLog.Infof("NRF App is terminated")
}
