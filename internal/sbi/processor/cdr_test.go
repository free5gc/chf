package processor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/free5gc/chf/cdr/cdrType"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/openapi/models"
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
		err := os.Remove(fileName)
		if err != nil && !os.IsNotExist(err) {
			t.Errorf("remove %s: %v", fileName, err)
		}
	})

	err := dumpCdrFile(ueid, nil)
	require.NoError(t, err)

	_, err = os.Stat(fileName)
	require.NoError(t, err)
}

func TestOpenCDRPartialRecordReturnsErrorWhenSessionMissing(t *testing.T) {
	p := &Processor{}

	ue := &chf_context.ChfUe{
		Supi: "imsi-208930000000951",
		Cdr:  map[string]*cdrType.CHFRecord{},
	}

	cdr, err := p.OpenCDR(models.Chf_ConvCharging_ChargingDataRequest{}, ue, "missing-session", true)
	require.Nil(t, cdr)
	require.Error(t, err)
}
