package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/free5gc/chf/internal/cgf"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	chargingdb "github.com/free5gc/chf/internal/db"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/sbi"
	"github.com/free5gc/chf/internal/sbi/consumer"
	"github.com/free5gc/chf/internal/sbi/processor"
	"github.com/free5gc/chf/pkg/abmf"
	"github.com/free5gc/chf/pkg/app"
	"github.com/free5gc/chf/pkg/factory"
	"github.com/free5gc/chf/pkg/rf"
	"github.com/free5gc/openapi"
	"github.com/free5gc/util/metrics"
	"github.com/free5gc/util/metrics/utils"
)

var CHF *ChfApp

var _ app.App = &ChfApp{}

type ChfApp struct {
	chfCtx *chf_context.CHFContext
	cfg    *factory.Config

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	sbiServer      *sbi.Server
	metricsServer  *metrics.Server
	consumer       *consumer.Consumer
	processor      *processor.Processor
	chargingDB     *mongo.Database
	chargingClient *mongo.Client
}

func NewApp(ctx context.Context, cfg *factory.Config, tlsKeyLogPath string) (*ChfApp, error) {
	if err := openapi.ConfigureHTTP2Clients(cfg.GetReadIdleTimeout(), cfg.GetPingTimeout()); err != nil {
		return nil, err
	}
	chf := &ChfApp{
		cfg: cfg,
		wg:  sync.WaitGroup{},
	}
	chf.SetLogEnable(cfg.GetLogEnable())
	chf.SetLogLevel(cfg.GetLogLevel())
	chf.SetReportCaller(cfg.GetLogReportCaller())
	chf_context.Init()

	dbCfg := cfg.Configuration.Mongodb
	if dbCfg == nil {
		return nil, fmt.Errorf("charging MongoDB configuration is missing")
	}
	dbCtx, dbCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dbCancel()
	chargingClient, dbErr := mongo.Connect(dbCtx, options.Client().ApplyURI(dbCfg.Url))
	if dbErr != nil {
		return nil, dbErr
	}
	chf.chargingClient = chargingClient
	chf.chargingDB = chargingClient.Database(dbCfg.Name)
	initialized := false
	defer func() {
		if !initialized {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			if closeErr := chargingClient.Disconnect(closeCtx); closeErr != nil {
				logger.InitLog.Errorf("Close charging MongoDB: %v", closeErr)
			}
		}
	}()
	if err := chargingdb.InitializeSchema(dbCtx, chf.chargingDB); err != nil {
		return nil, err
	}
	accounts := charging.NewAccounts(chf.chargingDB)
	if err := accounts.ImportLegacy(dbCtx); err != nil {
		return nil, err
	}
	processor, err_p := processor.NewProcessor(chf, charging.NewJournal(chf.chargingDB), accounts)
	if err_p != nil {
		return chf, err_p
	}
	chf.processor = processor

	consumer, err := consumer.NewConsumer(chf)
	if err != nil {
		return chf, err
	}
	chf.consumer = consumer

	chf.ctx, chf.cancel = context.WithCancel(ctx)
	chf.chfCtx = chf_context.GetSelf()

	if chf.sbiServer, err = sbi.NewServer(chf, tlsKeyLogPath); err != nil {
		return nil, err
	}

	features := map[utils.MetricTypeEnabled]bool{utils.SBI: true}
	customMetrics := make(map[utils.MetricTypeEnabled][]prometheus.Collector)
	if cfg.AreMetricsEnabled() {
		if chf.metricsServer, err = metrics.NewServer(
			getInitMetrics(cfg, features, customMetrics), tlsKeyLogPath, logger.InitLog, cfg.GetIdleTimeout(),
		); err != nil {
			return nil, err
		}
	}

	CHF = chf

	initialized = true
	return chf, nil
}

func getInitMetrics(
	cfg *factory.Config,
	features map[utils.MetricTypeEnabled]bool,
	customMetrics map[utils.MetricTypeEnabled][]prometheus.Collector,
) metrics.InitMetrics {
	metricsInfo := metrics.Metrics{
		BindingIPv4: cfg.GetMetricsBindingAddr(),
		Scheme:      cfg.GetMetricsScheme(),
		Namespace:   cfg.GetMetricsNamespace(),
		Port:        cfg.GetMetricsPort(),
		Tls: metrics.Tls{
			Key: cfg.GetMetricsCertKeyPath(),
			Pem: cfg.GetMetricsCertPemPath(),
		},
	}

	return metrics.NewInitMetrics(metricsInfo, "chf", features, customMetrics)
}

func (a *ChfApp) CancelContext() context.Context {
	return a.ctx
}

func (a *ChfApp) Consumer() *consumer.Consumer {
	return a.consumer
}

func (a *ChfApp) Processor() *processor.Processor {
	return a.processor
}

func (a *ChfApp) Context() *chf_context.CHFContext {
	return a.chfCtx
}

func (a *ChfApp) Config() *factory.Config {
	return a.cfg
}

func (c *ChfApp) SetLogEnable(enable bool) {
	logger.MainLog.Infof("Log enable is set to [%v]", enable)
	if enable && logger.Log.Out == os.Stderr {
		return
	} else if !enable && logger.Log.Out == io.Discard {
		return
	}

	c.Config().SetLogEnable(enable)
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

	c.Config().SetLogLevel(level)
	logger.Log.SetLevel(lvl)
}

func (c *ChfApp) SetReportCaller(reportCaller bool) {
	logger.MainLog.Infof("Report Caller is set to [%v]", reportCaller)
	if reportCaller == logger.Log.ReportCaller {
		return
	}
	c.Config().SetLogReportCaller(reportCaller)
	logger.Log.SetReportCaller(reportCaller)
}

func (a *ChfApp) Start() {
	logger.InitLog.Infoln("Server started")

	if a.cfg.Configuration.Cgf.Enable {
		cgf.CGFEnable = true
		a.wg.Add(1)
		cgf.OpenServer(a.ctx, &a.wg)
	}

	a.wg.Add(1)
	rf.OpenServer(a.ctx, &a.wg)

	a.wg.Add(1)
	abmf.OpenServer(a.ctx, &a.wg, charging.NewAccounts(a.chargingDB))

	a.wg.Add(1)
	go a.listenShutdownEvent()

	if err := a.sbiServer.Run(context.Background(), &a.wg); err != nil {
		logger.MainLog.Fatalf("Run SBI server failed: %+v", err)
	}

	if a.cfg.AreMetricsEnabled() && a.metricsServer != nil {
		go func() {
			a.metricsServer.Run(&a.wg)
		}()
	}

	a.WaitRoutineStopped()
}

func (a *ChfApp) listenShutdownEvent() {
	defer func() {
		if p := recover(); p != nil {
			// Print stack for panic to log. Fatalf() will let program exit.
			logger.MainLog.Fatalf("panic: %v\n%s", p, string(debug.Stack()))
		}
		a.wg.Done()
	}()

	<-a.ctx.Done()
	a.terminateProcedure()
}

func (c *ChfApp) Terminate() {
	c.cancel()
}

func (c *ChfApp) terminateProcedure() {
	logger.MainLog.Infof("Terminating CHF...")
	c.CallServerStop()

	// deregister with NRF
	problemDetails, err := c.Consumer().SendDeregisterNFInstance()
	if problemDetails != nil {
		logger.MainLog.Errorf("Deregister NF instance Failed Problem[%+v]", problemDetails)
	} else if err != nil {
		logger.MainLog.Errorf("Deregister NF instance Error[%+v]", err)
	} else {
		logger.MainLog.Infof("Deregister from NRF successfully")
	}
	logger.MainLog.Infof("CHF SBI Server terminated")
}

func (a *ChfApp) CallServerStop() {
	if a.sbiServer != nil {
		a.sbiServer.Stop()
	}

	if a.metricsServer != nil {
		a.metricsServer.Stop()
		logger.MainLog.Infof("CHF Metrics Server terminated")
	}
}

func (a *ChfApp) WaitRoutineStopped() {
	a.wg.Wait()
	if a.chargingClient != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := a.chargingClient.Disconnect(closeCtx); err != nil {
			logger.MainLog.Errorf("Close charging MongoDB: %v", err)
		}
	}
	logger.MainLog.Infof("CHF App is terminated")
}
