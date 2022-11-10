// ftpserver allows to create your own FTP(S) server
package ftp

import (
	"io/ioutil"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fclairamb/ftpserver/config"
	"github.com/fclairamb/ftpserver/server"
	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/free5gc/chf/internal/logger"
)

var (
	ftpServer *ftpserver.FtpServer
	driver    *server.Server
)

func OpenServer() {
	// Arguments vars
	var confFile string
	var onlyConf bool

	logger.FtpLog.Info("Start FTP server")
	autoCreate := onlyConf
	// The general idea here is that if you start it without any arg, you're probably doing a local quick&dirty run
	// possibly on a windows machine, so we're better of just using a default file name and create the file.
	if confFile == "" {
		confFile = "ftpserver.json"
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

		return
	}

	// Loading the driver
	var errNewServer error
	driver, errNewServer = server.NewServer(conf, logger.FtpServerLog)

	if errNewServer != nil {
		logger.FtpLog.Error("Could not load the driver", "err", errNewServer)

		return
	}

	// Instantiating the server by passing our driver implementation
	ftpServer = ftpserver.NewFtpServer(driver)

	// Setting up the ftpserver logger
	ftpServer.Logger = logger.FtpServerLog

	// Preparing the SIGTERM handling
	go signalHandler()

	// Blocking call, behaving similarly to the http.ListenAndServe
	if onlyConf {
		logger.FtpLog.Warn("Only creating conf")

		return
	}

	go func() {
		defer stop()
		if err := ftpServer.ListenAndServe(); err != nil {
			logger.FtpLog.Error("Problem listening", "err", err)
		}

		// We wait at most 1 minutes for all clients to disconnect
		if err := driver.WaitGracefully(time.Minute); err != nil {
			ftpServer.Logger.Warn("Problem stopping server", "Err", err)
		}
	}()

}

func stop() {
	driver.Stop()

	if err := ftpServer.Stop(); err != nil {
		ftpServer.Logger.Error("Problem stopping server", "Err", err)
	}
}

func signalHandler() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)

	for {
		sig := <-ch

		if sig == syscall.SIGTERM {
			stop()

			break
		}
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
