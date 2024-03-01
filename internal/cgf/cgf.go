// ftpserver allows to create your own FTP(S) server
package cgf

import (
	"bytes"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/fclairamb/ftpserver/config"
	"github.com/fclairamb/ftpserver/server"
	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/pkg/factory"
	"github.com/jlaffaye/ftp"
)

type Cgf struct {
	ftpServer *ftpserver.FtpServer
	driver    *server.Server
	conn      *ftp.ServerConn
	addr      string
	ftpConfig FtpConfig

	connMutex sync.Mutex
}

type Access struct {
	User   string            `json:"user"`
	Pass   string            `json:"pass"`
	Fs     string            `json:"fs"`
	Params map[string]string `json:"params"`
}

type FtpConfig struct {
	Version       int      `json:"version"`
	Accesses      []Access `json:"accesses"`
	ListenAddress string   `json:"listen_address"`
}

var cgf *Cgf

func OpenServer(wg *sync.WaitGroup) *Cgf {
	// Arguments vars
	cgf = new(Cgf)

	cgfConfig := factory.ChfConfig.Configuration.Cgf
	cgf.addr = cgfConfig.HostIPv4 + ":" + strconv.Itoa(cgfConfig.Port)

	cgf.ftpConfig = FtpConfig{
		Version: 1,
		Accesses: []Access{
			{
				User: "admin",
				Pass: "free5gc",
				Fs:   "os",
				Params: map[string]string{
					"basePath": "/tmp",
				},
			},
		},
		ListenAddress: factory.ChfConfig.Configuration.Sbi.RegisterIPv4 + ":" + strconv.Itoa(cgfConfig.ListenPort),
	}

	file, err := os.Create("/tmp/config.json")
	if err != nil {
		panic(err)
	}
	defer func() {
		if close_err := file.Close(); close_err != nil {
			logger.CfgLog.Error("Can't close file", close_err)
		}
	}()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cgf.ftpConfig); err != nil {
		panic(err)
	}

	conf, errConfig := config.NewConfig("/tmp/config.json", logger.FtpServerLog)
	if errConfig != nil {
		logger.CgfLog.Error("Can't load conf", "Err", errConfig)

		return nil
	}

	// Loading the driver
	var errNewServer error
	cgf.driver, errNewServer = server.NewServer(conf, logger.FtpServerLog)

	if errNewServer != nil {
		logger.CgfLog.Error("Could not load the driver", "err", errNewServer)

		return nil
	}

	// Instantiating the server by passing our driver implementation
	cgf.ftpServer = ftpserver.NewFtpServer(cgf.driver)

	// Setting up the ftpserver logger
	cgf.ftpServer.Logger = logger.FtpServerLog

	go cgf.Serve(wg)
	logger.CgfLog.Info("FTP server Start")

	return cgf
}

func Login() error {
	cgf.connMutex.Lock()
	defer cgf.connMutex.Unlock()

	if cgf.conn != nil {
		ping_err := cgf.conn.NoOp()
		if ping_err == nil {
			logger.CgfLog.Infof("FTP already login.")
			return nil
		}
	}

	// FTP server is for CDR transfer
	var c *ftp.ServerConn

	c, err := ftp.Dial(cgf.addr, ftp.DialWithTimeout(2*time.Second))
	if err != nil {
		return err
	}

	err = c.Login(cgf.ftpConfig.Accesses[0].User, cgf.ftpConfig.Accesses[0].Pass)
	if err != nil {
		logger.CgfLog.Warnf("Login FTP server fail")
		return err
	}

	logger.CgfLog.Info("Login FTP server succeed")
	cgf.conn = c
	return err
}

func SendCDR(supi string) error {
	logger.CfgLog.Debugln("SendCDR:", supi)
	if cgf.conn == nil {
		err := Login()

		if err != nil {
			return err
		}
		logger.CgfLog.Infof("FTP Re-Login Success")
	}

	ping_err := cgf.conn.NoOp()
	if ping_err != nil {
		logger.CgfLog.Infof("Faile to ping FTP server, relogin...")
		err := Login()

		if err != nil {
			return err
		}
		logger.CgfLog.Infof("FTP Re-Login Success")
	}

	fileName := supi + ".cdr"
	cdrByte, err := os.ReadFile("/tmp/" + fileName)
	if err != nil {
		return err
	}

	cdrReader := bytes.NewReader(cdrByte)
	stor_err := cgf.conn.Stor(fileName, cdrReader)
	if stor_err != nil {
		return err
	}

	return nil
}

const FTP_LOGIN_RETRY_NUMBER = 3
const FTP_LOGIN_RETRY_WAITING_TIME = 1 * time.Second // second

func (f *Cgf) Serve(wg *sync.WaitGroup) {
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
		f.Terminate()
		wg.Done()
		os.Exit(0)
	}()

	for i := 0; i < FTP_LOGIN_RETRY_NUMBER; i++ {
		if err := Login(); err != nil {
			logger.CgfLog.Warnf("Login to Webconsole FTP fail: %s, retrying [%d]\n", err, i+1)
			time.Sleep(FTP_LOGIN_RETRY_WAITING_TIME)
		} else {
			break
		}
	}

	if err := f.ftpServer.ListenAndServe(); err != nil {
		logger.CgfLog.Error("Problem listening", "err", err)
	}

	// We wait at most 1 minutes for all clients to disconnect
	if err := f.driver.WaitGracefully(time.Minute); err != nil {
		logger.CgfLog.Warn("Problem stopping server", "Err", err)
	}
}

func (f *Cgf) Terminate() {
	f.driver.Stop()

	if err := f.ftpServer.Stop(); err != nil {
		logger.CgfLog.Error("Problem stopping server", "Err", err)
	}

	var cdrFilePath string
	if factory.ChfConfig.Configuration.Cgf.CdrFilePath == "" {
		cdrFilePath = factory.CgfDefaultCdrFilePath
	} else {
		cdrFilePath = factory.ChfConfig.Configuration.Cgf.CdrFilePath
	}
	files, err := filepath.Glob(cdrFilePath + "/*.cdr")
	if err != nil {
		logger.CgfLog.Warnln("no CDR file")
	}

	for _, file := range files {
		if _, err := os.Stat(file); err == nil {
			logger.CgfLog.Infof("Remove CDR file: " + file)
			if err := os.Remove(file); err != nil {
				logger.CgfLog.Warnf("Failed to remove CDR file: %s\n", file)
			}
		}
	}
}
