package abmf

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/fiorix/go-diameter/diam"
	"github.com/stretchr/testify/require"

	charging_code "github.com/free5gc/chf/ccs_diameter/code"
	"github.com/free5gc/chf/internal/charging"
)

func TestAccountResultCode(t *testing.T) {
	require.Equal(t, uint32(diam.Success), accountResultCode(nil))
	require.Equal(t, uint32(charging_code.DiameterCreditLimitReached),
		accountResultCode(fmt.Errorf("apply: %w", charging.ErrNoBalance)))
	require.Equal(t, uint32(diam.UnableToComply), accountResultCode(context.DeadlineExceeded))
	require.Equal(t, uint32(diam.UnableToComply), accountResultCode(errors.New("account database unavailable")))
}
