package cgf

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fclairamb/ftpserver/config"
	"github.com/fclairamb/ftpserver/server"
	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/stretchr/testify/require"

	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/pkg/factory"
)

// newTestCgf builds a CGF the way OpenServer does, without serving, with its
// FTP server rooted in dir.
func newTestCgf(t *testing.T, dir string) *Cgf {
	t.Helper()
	confFile := filepath.Join(t.TempDir(), "config.json")
	conf := `{"version": 1, "listen_address": "127.0.0.1:0", "accesses": [` +
		`{"user": "admin", "pass": "free5gc", "fs": "os", "params": {"basePath": "` + dir + `"}}]}`
	require.NoError(t, os.WriteFile(confFile, []byte(conf), 0o600))

	ftpConf, err := config.NewConfig(confFile, logger.FtpServerLog)
	require.NoError(t, err)
	driver, err := server.NewServer(ftpConf, logger.FtpServerLog)
	require.NoError(t, err)

	return &Cgf{driver: driver, ftpServer: ftpserver.NewFtpServer(driver)}
}

func TestTerminateKeepsCdrFiles(t *testing.T) {
	dir := t.TempDir()

	saved := factory.ChfConfig
	t.Cleanup(func() { factory.ChfConfig = saved })
	factory.ChfConfig = &factory.Config{
		Configuration: &factory.Configuration{Cgf: &factory.Cgf{CdrFilePath: dir}},
	}

	cdrFile := filepath.Join(dir, buildCdrFileName("imsi-208930000000001"))
	content := []byte("not yet delivered")
	require.NoError(t, os.WriteFile(cdrFile, content, 0o600))

	newTestCgf(t, dir).Terminate()

	got, err := os.ReadFile(cdrFile)
	require.NoError(t, err, "Terminate removed an undelivered CDR file")
	require.Equal(t, content, got)
}
