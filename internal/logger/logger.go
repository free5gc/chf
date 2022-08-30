package logger

import (
	"os"
	"time"

	formatter "github.com/antonfisher/nested-logrus-formatter"
	"github.com/sirupsen/logrus"

	logger_util "github.com/free5gc/util/logger"
)

var (
	log                 *logrus.Logger
	AppLog              *logrus.Entry
	InitLog             *logrus.Entry
	CfgLog              *logrus.Entry
	CtxLog              *logrus.Entry
	UtilLog             *logrus.Entry
	ConsumerLog         *logrus.Entry
	GinLog              *logrus.Entry
	ChargingdataPostLog *logrus.Entry
	NotifyEventLog      *logrus.Entry
	RechargingLog       *logrus.Entry
)

func init() {
	log = logrus.New()
	log.SetReportCaller(false)

	log.Formatter = &formatter.Formatter{
		TimestampFormat: time.RFC3339,
		TrimMessages:    true,
		NoFieldsSpace:   true,
		HideKeys:        true,
		FieldsOrder:     []string{"component", "category"},
	}

	AppLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "App"})
	InitLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "Init"})
	CfgLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "CFG"})
	UtilLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "Util"})
	ConsumerLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "Consumer"})
	CtxLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "Context"})
	ConsumerLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "Consumer"})
	GinLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "GIN"})
	ChargingdataPostLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "ChargingdataPost"})
	NotifyEventLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "NotifyEventLog"})
	RechargingLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "RechargingLog"})
}

func LogFileHook(logNfPath string, log5gcPath string) error {
	if fullPath, err := logger_util.CreateFree5gcLogFile(log5gcPath); err == nil {
		if fullPath != "" {
			free5gcLogHook, hookErr := logger_util.NewFileHook(fullPath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o666)
			if hookErr != nil {
				return hookErr
			}
			log.Hooks.Add(free5gcLogHook)
		}
	} else {
		return err
	}

	if fullPath, err := logger_util.CreateNfLogFile(logNfPath, "chf.log"); err == nil {
		selfLogHook, hookErr := logger_util.NewFileHook(fullPath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o666)
		if hookErr != nil {
			return hookErr
		}
		log.Hooks.Add(selfLogHook)
	} else {
		return err
	}

	return nil
}

func SetLogLevel(level logrus.Level) {
	log.SetLevel(level)
}

func SetReportCaller(enable bool) {
	log.SetReportCaller(enable)
}
