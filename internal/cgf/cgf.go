// ftpserver allows to create your own FTP(S) server
package cgf

import (
	"bytes"
	"io/ioutil"
	"os"
	"sync"
	"time"

	"github.com/fclairamb/ftpserver/config"
	"github.com/fclairamb/ftpserver/server"
	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/free5gc/chf/internal/logger"
	"github.com/jlaffaye/ftp"
)

type FTPServer struct {
	ftpServer *ftpserver.FtpServer
	driver    *server.Server
	conn      *ftp.ServerConn
}

var ftpServ *FTPServer

func OpenServer(wg *sync.WaitGroup) *FTPServer {
	// Arguments vars
	var confFile string
	var autoCreate bool

	ftpServ = new(FTPServer)

	if confFile == "" {
		confFile = "/tmp/ftpserver.json"
		autoCreate = true
	}

	if autoCreate {
		if _, err := os.Stat(confFile); err != nil && os.IsNotExist(err) {
			logger.CgfLog.Warn("No conf file, creating one", confFile)

			if err := ioutil.WriteFile(confFile, confFileContent(), 0600); err != nil { //nolint: gomnd
				logger.CgfLog.Warn("Couldn't create conf file", confFile)
			}
		}
	}

	conf, errConfig := config.NewConfig(confFile, logger.FtpServerLog)
	if errConfig != nil {
		logger.CgfLog.Error("Can't load conf", "Err", errConfig)

		return nil
	}

	// Loading the driver
	var errNewServer error
	ftpServ.driver, errNewServer = server.NewServer(conf, logger.FtpServerLog)

	if errNewServer != nil {
		logger.CgfLog.Error("Could not load the driver", "err", errNewServer)

		return nil
	}

	// Instantiating the server by passing our driver implementation
	ftpServ.ftpServer = ftpserver.NewFtpServer(ftpServ.driver)

	// Setting up the ftpserver logger
	ftpServ.ftpServer.Logger = logger.FtpServerLog

	go ftpServ.Serve(wg)
	logger.CgfLog.Info("FTP server Start")

	return ftpServ
}

func Login() error {
	// FTP server is for CDR transfer
	var c *ftp.ServerConn

	c, err := ftp.Dial("127.0.0.1:2122", ftp.DialWithTimeout(5*time.Second))
	if err != nil {
		return err
	}

	err = c.Login("admin", "free5gc")
	if err != nil {
		logger.CgfLog.Warnf("Login FTP server Fail")
		return err
	}

	logger.CgfLog.Info("Login FTP server")
	ftpServ.conn = c
	return err
}

func SendCDR(supi string) error {
	if ftpServ.conn == nil {
		err := Login()

		if err != nil {
			return err
		}
		logger.ChargingdataPostLog.Infof("FTP Re-Login Success")

	}

	fileName := supi + ".cdr"
	cdrByte, err := os.ReadFile("/tmp/" + fileName)
	if err != nil {
		return err
	}

	cdrReader := bytes.NewReader(cdrByte)
	ftpServ.conn.Stor(fileName, cdrReader)

	return nil
}

func (f *FTPServer) Serve(wg *sync.WaitGroup) {
	defer func() {
		logger.CgfLog.Error("FTP server stopped")
		f.Stop()
		wg.Done()
	}()

	if err := Login(); err != nil {
		logger.CgfLog.Error("Login to Webconsole FTP fail", "err", err)
	}

	if err := f.ftpServer.ListenAndServe(); err != nil {
		logger.CgfLog.Error("Problem listening", "err", err)
	}

	// We wait at most 1 minutes for all clients to disconnect
	if err := f.driver.WaitGracefully(time.Minute); err != nil {
		logger.CgfLog.Warn("Problem stopping server", "Err", err)
	}
}

func (f *FTPServer) Stop() {
	f.driver.Stop()

	if err := f.ftpServer.Stop(); err != nil {
		logger.CgfLog.Error("Problem stopping server", "Err", err)
	}
}

func confFileContent() []byte {
	str := `{
  "version": 1,
  "accesses": [
    {
      "user": "admin",
      "pass": "free5gc",
      "fs": "os",
      "params": {
        "basePath": "/tmp"
      }
    }
  ],
  "listen_address": "127.0.0.113:2121"
}`

	return []byte(str)
}
