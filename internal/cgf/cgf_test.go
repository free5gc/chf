package cgf

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// testDriver serves fs to the CGF's credentials.
type testDriver struct {
	fs afero.Fs
}

func (d *testDriver) GetSettings() (*ftpserver.Settings, error) {
	return &ftpserver.Settings{ListenAddr: "127.0.0.1:0"}, nil
}

func (d *testDriver) ClientConnected(ftpserver.ClientContext) (string, error) {
	return "test billing server", nil
}

func (d *testDriver) ClientDisconnected(ftpserver.ClientContext) {}

func (d *testDriver) AuthUser(_ ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	if user != "admin" || pass != "free5gc" {
		return nil, errors.New("bad credentials")
	}
	return d.fs, nil
}

func (d *testDriver) GetTLSConfig() (*tls.Config, error) {
	return nil, errors.New("no TLS")
}

// startBillingServer runs an FTP server on fs and points the CGF at it.
func startBillingServer(t *testing.T, fs afero.Fs) {
	t.Helper()
	server := ftpserver.NewFtpServer(&testDriver{fs: fs})
	require.NoError(t, server.Listen())
	served := make(chan error, 1)
	go func() { served <- server.Serve() }()
	t.Cleanup(func() {
		if err := server.Stop(); err != nil {
			t.Logf("stop FTP server: %v", err)
		}
	})
	useBillingServer(t, server.Addr())
}

func useBillingServer(t *testing.T, addr string) {
	t.Helper()
	prevCgf, prevEnable := cgf, CGFEnable
	cgf = &Cgf{
		addr:      addr,
		ftpConfig: FtpConfig{Accesses: []Access{{User: "admin", Pass: "free5gc"}}},
	}
	CGFEnable = true
	t.Cleanup(func() {
		if cgf.conn != nil {
			if err := cgf.conn.Quit(); err != nil {
				t.Logf("quit FTP connection: %v", err)
			}
		}
		cgf, CGFEnable = prevCgf, prevEnable
	})
}

// testSupi is unique per test process: SendCDR reads the CDR file from a fixed
// directory (/tmp), so concurrent test binaries must not share names.
func testSupi(n int) string {
	return fmt.Sprintf("imsi-20893%05d%05d", os.Getpid()%100000, n)
}

// writeCdrFile creates the local CDR file SendCDR uploads for supi.
func writeCdrFile(t *testing.T, supi string) string {
	t.Helper()
	path := "/tmp/" + buildCdrFileName(supi)
	require.NoError(t, os.WriteFile(path, []byte("cdr for "+supi), 0o600))
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil {
			t.Logf("remove %s: %v", path, err)
		}
	})
	return path
}

func TestSendCDRReportsRefusedUpload(t *testing.T) {
	startBillingServer(t, afero.NewReadOnlyFs(afero.NewMemMapFs()))
	supi := testSupi(101)
	path := writeCdrFile(t, supi)

	err := SendCDR(supi)

	require.ErrorContains(t, err, "upload of", "a refused STOR must be reported")
	require.FileExists(t, path, "the CDR file must be kept for a later attempt")
}

// stallingFs never completes opening a file for writing, so the server stops
// answering in the middle of an upload.
type stallingFs struct {
	afero.Fs
	release chan struct{}
}

func (fs *stallingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		<-fs.release
	}
	return fs.Fs.OpenFile(name, flag, perm)
}

func TestSendCDRFailsWhenServerStopsAnswering(t *testing.T) {
	fs := &stallingFs{Fs: afero.NewMemMapFs(), release: make(chan struct{})}
	t.Cleanup(func() { close(fs.release) })
	startBillingServer(t, fs)
	supi := testSupi(102)
	writeCdrFile(t, supi)
	prevTimeout := ftpIOTimeout
	ftpIOTimeout = 500 * time.Millisecond
	t.Cleanup(func() { ftpIOTimeout = prevTimeout })

	done := make(chan error, 1)
	go func() { done <- SendCDR(supi) }()

	select {
	case err := <-done:
		// Failing in the upload, not in the login before it.
		require.ErrorContains(t, err, "upload of")
	case <-time.After(5 * time.Second):
		t.Fatal("SendCDR is still blocked on a server that stopped answering")
	}
}

func TestSendCDRConcurrent(t *testing.T) {
	fs := afero.NewMemMapFs()
	startBillingServer(t, fs)
	const sessions = 8
	for i := range sessions {
		writeCdrFile(t, testSupi(200+i))
	}

	var wg sync.WaitGroup
	errs := make([]error, sessions)
	for i := range sessions {
		wg.Go(func() { errs[i] = SendCDR(testSupi(200 + i)) })
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "session %d", i)
		delivered, errRead := afero.ReadFile(fs, "/"+buildCdrFileName(testSupi(200+i)))
		require.NoError(t, errRead, "session %d's CDR file was not delivered", i)
		require.Equal(t, "cdr for "+testSupi(200+i), string(delivered), "session %d's CDR file", i)
	}
}
