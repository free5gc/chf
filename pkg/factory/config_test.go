package factory

import (
	"testing"

	"github.com/asaskevich/govalidator"

	"github.com/free5gc/util/nfheartbeat"
)

func TestGetNfHeartBeatTimer(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want int32
	}{
		{
			name: "no configuration section",
			cfg:  &Config{},
			want: nfheartbeat.DefaultTimer,
		},
		{
			name: "option absent",
			cfg:  &Config{Configuration: &Configuration{}},
			want: nfheartbeat.DefaultTimer,
		},
		{
			name: "option set",
			cfg:  &Config{Configuration: &Configuration{NfHeartBeatTimer: 45}},
			want: 45,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.GetNfHeartBeatTimer(); got != tt.want {
				t.Errorf("GetNfHeartBeatTimer() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNfHeartBeatTimerRange(t *testing.T) {
	// The range(1|3600) struct tag cannot reference constants; keep it aligned
	// with the bounds the NRF profile validator enforces.
	if nfheartbeat.MinTimer != 1 || nfheartbeat.MaxTimer != 3600 {
		t.Fatalf("range(1|3600) tag out of sync with nfheartbeat bounds [%d, %d]",
			nfheartbeat.MinTimer, nfheartbeat.MaxTimer)
	}

	tests := []struct {
		name    string
		timer   int32
		wantErr bool
	}{
		{name: "absent is optional", timer: 0},
		{name: "lower bound", timer: nfheartbeat.MinTimer},
		{name: "upper bound of 1 hour", timer: nfheartbeat.MaxTimer},
		{name: "above the upper bound", timer: nfheartbeat.MaxTimer + 1, wantErr: true},
		{name: "negative", timer: -1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := govalidator.ValidateStruct(&Configuration{NfHeartBeatTimer: tt.timer})

			fieldErr := govalidator.ErrorByField(err, "NfHeartBeatTimer")
			if gotErr := fieldErr != ""; gotErr != tt.wantErr {
				t.Errorf("nfHeartBeatTimer %d: field error = %q, want error %v", tt.timer, fieldErr, tt.wantErr)
			}
		})
	}
}

func TestGetCgfFtpCredentials(t *testing.T) {
	tests := []struct {
		name         string
		cfg          *Config
		wantUser     string
		wantPassword string
	}{
		{
			name:         "no cgf section",
			cfg:          &Config{Configuration: &Configuration{}},
			wantUser:     "admin",
			wantPassword: "free5gc",
		},
		{
			name:         "credentials absent",
			cfg:          &Config{Configuration: &Configuration{Cgf: &Cgf{}}},
			wantUser:     "admin",
			wantPassword: "free5gc",
		},
		{
			name: "credentials set",
			cfg: &Config{Configuration: &Configuration{Cgf: &Cgf{
				FtpUser: "billing", FtpPassword: "s3cret",
			}}},
			wantUser:     "billing",
			wantPassword: "s3cret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, password := tt.cfg.GetCgfFtpCredentials()
			if user != tt.wantUser || password != tt.wantPassword {
				t.Errorf("GetCgfFtpCredentials() = %q, %q, want %q, %q",
					user, password, tt.wantUser, tt.wantPassword)
			}
		})
	}
}

func TestValidateFtpCredentials(t *testing.T) {
	tests := []struct {
		name    string
		cgf     Cgf
		wantErr bool
	}{
		{name: "neither", cgf: Cgf{}},
		{name: "both", cgf: Cgf{FtpUser: "billing", FtpPassword: "s3cret"}},
		{name: "user only", cgf: Cgf{FtpUser: "billing"}, wantErr: true},
		{name: "password only", cgf: Cgf{FtpPassword: "s3cret"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cgf.validateFtpCredentials(); (err != nil) != tt.wantErr {
				t.Errorf("validateFtpCredentials() = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}
