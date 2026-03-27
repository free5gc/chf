package processor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildCdrFileNameIsBounded(t *testing.T) {
	ueid := "imsi-" + strings.Repeat("a", 1024)

	fileName := buildCdrFileName(ueid)

	require.True(t, strings.HasPrefix(fileName, "/tmp/chf-"))
	require.True(t, strings.HasSuffix(fileName, ".cdr"))
	require.LessOrEqual(t, len(filepath.Base(fileName)), 255)
}

func TestDumpCdrFileWithLongSubscriberIdentifier(t *testing.T) {
	ueid := "imsi-" + strings.Repeat("b", 1024)
	fileName := buildCdrFileName(ueid)
	t.Cleanup(func() {
		_ = os.Remove(fileName)
	})

	err := dumpCdrFile(ueid, nil)
	require.NoError(t, err)

	_, err = os.Stat(fileName)
	require.NoError(t, err)
}
