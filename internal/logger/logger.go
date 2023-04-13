package logger

import (
	"os"
	"time"

	formatter "github.com/antonfisher/nested-logrus-formatter"
	golog "github.com/fclairamb/go-log"
	adapter "github.com/fclairamb/go-log/logrus"
	logger_util "github.com/free5gc/util/logger"
	"github.com/sirupsen/logrus"
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
	RatingLog           *logrus.Entry
	FtpLog              *logrus.Entry
	FtpServerLog        golog.Logger
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
	NotifyEventLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "NotifyEvent"})
	RechargingLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "Recharging"})
	FtpLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "FTP"})
	RatingLog = log.WithFields(logrus.Fields{"component": "CHF", "category": "Rating"})
	FtpServerLog = adapter.NewWrap(FtpLog.Logger).With("component", "CHF", "category", "FTP")
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
