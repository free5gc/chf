// ftpserver allows to create your own FTP(S) server
package ftp

import (
	"io/ioutil"
	"os"
	"sync"
	"time"

	"github.com/fclairamb/ftpserver/config"
	"github.com/fclairamb/ftpserver/server"
	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/free5gc/chf/internal/logger"
)

type FTPServer struct {
	ftpServer *ftpserver.FtpServer
	driver    *server.Server
}

func OpenServer(wg *sync.WaitGroup) *FTPServer {
	// Arguments vars
	var confFile string
	var autoCreate bool

	f := &FTPServer{}

	if confFile == "" {
		confFile = "/tmp/ftpserver.json"
		autoCreate = true

	}

	if autoCreate {
		if _, err := os.Stat(confFile); err != nil && os.IsNotExist(err) {
			logger.FtpLog.Warn("No conf file, creating one", confFile)

			if err := ioutil.WriteFile(confFile, confFileContent(), 0600); err != nil { //nolint: gomnd
				logger.FtpLog.Warn("Couldn't create conf file", confFile)
			}
		}
	}

	conf, errConfig := config.NewConfig(confFile, logger.FtpServerLog)
	if errConfig != nil {
		logger.FtpLog.Error("Can't load conf", "Err", errConfig)

		return nil
	}

	// Loading the driver
	var errNewServer error
	f.driver, errNewServer = server.NewServer(conf, logger.FtpServerLog)

	if errNewServer != nil {
		logger.FtpLog.Error("Could not load the driver", "err", errNewServer)

		return nil
	}

	// Instantiating the server by passing our driver implementation
	f.ftpServer = ftpserver.NewFtpServer(f.driver)

	// Setting up the ftpserver logger
	f.ftpServer.Logger = logger.FtpServerLog

	go f.Serve(wg)
	logger.FtpLog.Info(" FTP server Start")

	return f
}

func (f *FTPServer) Serve(wg *sync.WaitGroup) {
	defer func() {
		logger.FtpLog.Error("FTP server stopped")
		f.Stop()
		wg.Done()
	}()

	if err := f.ftpServer.ListenAndServe(); err != nil {
		logger.FtpLog.Error("Problem listening", "err", err)
	}

	// We wait at most 1 minutes for all clients to disconnect
	if err := f.driver.WaitGracefully(time.Minute); err != nil {
		logger.FtpLog.Warn("Problem stopping server", "Err", err)
	}
}

func (f *FTPServer) Stop() {
	f.driver.Stop()

	if err := f.ftpServer.Stop(); err != nil {
		logger.FtpLog.Error("Problem stopping server", "Err", err)
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
