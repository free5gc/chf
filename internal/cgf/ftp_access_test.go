package cgf

import (
	"testing"

	"github.com/free5gc/chf/pkg/factory"
)

func TestFtpAccessCarriesConfiguredCredentials(t *testing.T) {
	prev := factory.ChfConfig
	t.Cleanup(func() { factory.ChfConfig = prev })

	factory.ChfConfig = &factory.Config{Configuration: &factory.Configuration{Cgf: &factory.Cgf{}}}
	if a := ftpAccess(); a.User != "admin" || a.Pass != "free5gc" {
		t.Errorf("default access = %q/%q, want admin/free5gc", a.User, a.Pass)
	}

	factory.ChfConfig = &factory.Config{Configuration: &factory.Configuration{Cgf: &factory.Cgf{
		FtpUser: "billing", FtpPassword: "s3cret",
	}}}
	if a := ftpAccess(); a.User != "billing" || a.Pass != "s3cret" {
		t.Errorf("configured access = %q/%q, want billing/s3cret", a.User, a.Pass)
	}
}
