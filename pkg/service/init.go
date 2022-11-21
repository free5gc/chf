package service

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"

	"github.com/gin-contrib/cors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"

	"github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/ftp"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/sbi/consumer"
	"github.com/free5gc/chf/internal/sbi/convergedcharging"
	"github.com/free5gc/chf/internal/util"
	"github.com/free5gc/chf/pkg/factory"
	"github.com/free5gc/util/httpwrapper"
	logger_util "github.com/free5gc/util/logger"
)

type CHF struct {
	KeyLogPath string
}

type (
	// Commands information.
	Commands struct {
		config string
	}
)

var commands Commands

var cliCmd = []cli.Flag{
	cli.StringFlag{
		Name:  "config, c",
		Usage: "Load configuration from `FILE`",
	},
	cli.StringFlag{
		Name:  "log, l",
		Usage: "Output NF log to `FILE`",
	},
	cli.StringFlag{
		Name:  "log5gc, lc",
		Usage: "Output free5gc log to `FILE`",
	},
}

func (*CHF) GetCliCmd() (flags []cli.Flag) {
	return cliCmd
}

func (chf *CHF) Initialize(c *cli.Context) error {
	commands = Commands{
		config: c.String("config"),
	}

	if commands.config != "" {
		if err := factory.InitConfigFactory(commands.config); err != nil {
			return err
		}
	} else {
		if err := factory.InitConfigFactory(util.ChfDefaultConfigPath); err != nil {
			return err
		}
	}

	if err := factory.CheckConfigVersion(); err != nil {
		return err
	}

	if _, err := factory.ChfConfig.Validate(); err != nil {
		return err
	}

	chf.setLogLevel()

	return nil
}

func (chf *CHF) setLogLevel() {
	if factory.ChfConfig.Logger == nil {
		logger.InitLog.Warnln("CHF config without log level setting!!!")
		return
	}

	if factory.ChfConfig.Logger.CHF != nil {
		if factory.ChfConfig.Logger.CHF.DebugLevel != "" {
			if level, err := logrus.ParseLevel(factory.ChfConfig.Logger.CHF.DebugLevel); err != nil {
				logger.InitLog.Warnf("CHF Log level [%s] is invalid, set to [info] level",
					factory.ChfConfig.Logger.CHF.DebugLevel)
				logger.SetLogLevel(logrus.InfoLevel)
			} else {
				logger.InitLog.Infof("CHF Log level is set to [%s] level", level)
				logger.SetLogLevel(level)
			}
		} else {
			logger.InitLog.Infoln("CHF Log level is default set to [info] level")
			logger.SetLogLevel(logrus.InfoLevel)
		}
		logger.SetReportCaller(factory.ChfConfig.Logger.CHF.ReportCaller)
	}
}

func (chf *CHF) FilterCli(c *cli.Context) (args []string) {
	for _, flag := range chf.GetCliCmd() {
		name := flag.GetName()
		value := fmt.Sprint(c.Generic(name))
		if value == "" {
			continue
		}

		args = append(args, "--"+name, value)
	}
	return args
}

func (chf *CHF) Start() {
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

	pemPath := util.ChfDefaultPemPath
	keyPath := util.ChfDefaultKeyPath
	sbi := factory.ChfConfig.Configuration.Sbi
	if sbi.Tls != nil {
		pemPath = sbi.Tls.Pem
		keyPath = sbi.Tls.Key
	}

	self := context.CHF_Self()
	util.InitchfContext(self)

	addr := fmt.Sprintf("%s:%d", self.BindingIPv4, self.SBIPort)

	wg := sync.WaitGroup{}

	wg.Add(1)
	ftp.OpenServer(&wg)

	profile, err := consumer.BuildNFInstance(self)
	if err != nil {
		logger.InitLog.Error("Build CHF Profile Error")
	}
	_, self.NfId, err = consumer.SendRegisterNFInstance(self.NrfUri, self.NfId, profile)
	if err != nil {
		logger.InitLog.Errorf("CHF register to NRF Error[%s]", err.Error())
	}

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
		chf.Terminate()
		os.Exit(0)
	}()

	server, err := httpwrapper.NewHttp2Server(addr, chf.KeyLogPath, router)
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

func (chf *CHF) Exec(c *cli.Context) error {
	logger.InitLog.Traceln("args:", c.String("chfcfg"))
	args := chf.FilterCli(c)
	logger.InitLog.Traceln("filter: ", args)
	command := exec.Command("./chf", args...)

	stdout, err := command.StdoutPipe()
	if err != nil {
		logger.InitLog.Fatalln(err)
	}
	wg := sync.WaitGroup{}
	wg.Add(3)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				// Print stack for panic to log. Fatalf() will let program exit.
				logger.InitLog.Fatalf("panic: %v\n%s", p, string(debug.Stack()))
			}
		}()

		in := bufio.NewScanner(stdout)
		for in.Scan() {
			fmt.Println(in.Text())
		}
		wg.Done()
	}()

	stderr, err := command.StderrPipe()
	if err != nil {
		logger.InitLog.Fatalln(err)
	}
	go func() {
		defer func() {
			if p := recover(); p != nil {
				// Print stack for panic to log. Fatalf() will let program exit.
				logger.InitLog.Fatalf("panic: %v\n%s", p, string(debug.Stack()))
			}
		}()

		in := bufio.NewScanner(stderr)
		fmt.Println("CHF log start")
		for in.Scan() {
			fmt.Println(in.Text())
		}
		wg.Done()
	}()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				// Print stack for panic to log. Fatalf() will let program exit.
				logger.InitLog.Fatalf("panic: %v\n%s", p, string(debug.Stack()))
			}
		}()

		fmt.Println("CHF start")
		if err = command.Start(); err != nil {
			fmt.Printf("command.Start() error: %v", err)
		}
		fmt.Println("CHF end")
		wg.Done()
	}()

	wg.Wait()

	return err
}

func (chf *CHF) Terminate() {
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
