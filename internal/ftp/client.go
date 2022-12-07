package ftp

import (
	"time"

	"github.com/free5gc/chf/internal/logger"
	"github.com/jlaffaye/ftp"
)

func FTPLogin() (*ftp.ServerConn, error) {
	// FTP server is for CDR transfer
	var c *ftp.ServerConn

	c, err := ftp.Dial("127.0.0.1:2122", ftp.DialWithTimeout(5*time.Second))
	if err != nil {
		return nil, err
	}

	err = c.Login("admin", "free5gc")
	if err != nil {
		logger.FtpLog.Warnf("Login FTP server Fail")
		return nil, err
	}

	logger.FtpLog.Info("Login FTP server")
	return c, err
}
