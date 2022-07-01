/*
 * CHF Configuration Factory
 */

package factory

import (
	"fmt"

	"github.com/asaskevich/govalidator"

	tarrif_asn "github.com/free5gc/TarrifUtil/asn"
	logger_util "github.com/free5gc/util/logger"
)

const (
	ChfExpectedConfigVersion = "1.0.1"
	ChfSbiDefaultIPv4        = "127.0.0.113"
	ChfSbiDefaultPort        = 8000
)

type Config struct {
	Info          *Info               `yaml:"info" valid:"required"`
	Configuration *Configuration      `yaml:"configuration" valid:"required"`
	Logger        *logger_util.Logger `yaml:"logger" valid:"required"`
}

func (c *Config) Validate() (bool, error) {
	info := c.Info
	if _, err := info.validate(); err != nil {
		return false, err
	}

	Configuration := c.Configuration
	if _, err := Configuration.validate(); err != nil {
		return false, err
	}

	Logger := c.Logger
	if _, err := Logger.Validate(); err != nil {
		return false, err
	}

	if _, err := govalidator.ValidateStruct(c); err != nil {
		return false, appendInvalid(err)
	}

	return true, nil
}

type Info struct {
	Version     string `yaml:"version,omitempty" valid:"required"`
	Description string `yaml:"description,omitempty" valid:"-"`
}

func (i *Info) validate() (bool, error) {
	if _, err := govalidator.ValidateStruct(i); err != nil {
		return false, appendInvalid(err)
	}

	return true, nil
}

type Configuration struct {
	ChfName     string    `yaml:"chfName,omitempty" valid:"required, type(string)"`
	Sbi         *Sbi      `yaml:"sbi,omitempty" valid:"required"`
	NrfUri      string    `yaml:"nrfUri,omitempty" valid:"required, url"`
	ServiceList []Service `yaml:"serviceList,omitempty" valid:"required"`
	Tarrif      *Tarrif   `yaml:"tarrif,omitempty" valid:"required"`
}

func (c *Configuration) validate() (bool, error) {
	if c.Sbi != nil {
		if _, err := c.Sbi.validate(); err != nil {
			return false, err
		}
	}

	if c.ServiceList != nil {
		var errs govalidator.Errors
		for _, v := range c.ServiceList {
			if _, err := v.validate(); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return false, error(errs)
		}
	}

	if _, err := govalidator.ValidateStruct(c); err != nil {
		return false, appendInvalid(err)
	}

	return true, nil
}

type Service struct {
	ServiceName string `yaml:"serviceName" valid:"required, service"`
	SuppFeat    string `yaml:"suppFeat,omitempty" valid:"-"`
}

func (s *Service) validate() (bool, error) {
	govalidator.TagMap["service"] = govalidator.Validator(func(str string) bool {
		switch str {
		case "nchf-convergedcharging":
		default:
			return false
		}
		return true
	})

	if _, err := govalidator.ValidateStruct(s); err != nil {
		return false, appendInvalid(err)
	}

	return true, nil
}

type Sbi struct {
	Scheme       string `yaml:"scheme" valid:"required,scheme"`
	RegisterIPv4 string `yaml:"registerIPv4,omitempty" valid:"required,host"` // IP that is registered at NRF.
	// IPv6Addr  string `yaml:"ipv6Addr,omitempty"`
	BindingIPv4 string `yaml:"bindingIPv4,omitempty" valid:"required,host"` // IP used to run the server in the node.
	Port        int    `yaml:"port,omitempty" valid:"required,port"`
	Tls         *Tls   `yaml:"tls,omitempty" valid:"optional"`
}

func (s *Sbi) validate() (bool, error) {
	govalidator.TagMap["scheme"] = govalidator.Validator(func(str string) bool {
		return str == "https" || str == "http"
	})

	if tls := s.Tls; tls != nil {
		if result, err := tls.validate(); err != nil {
			return result, err
		}
	}

	if _, err := govalidator.ValidateStruct(s); err != nil {
		return false, appendInvalid(err)
	}

	return true, nil
}

type Tarrif struct {
	CCUnitType         CCUnitType       `yaml:"ccUnitType" valid:"required"`
	ChargeReasonCode   ChargeReasonCode `yaml:"chargeReasonCode,omitempty" valid:"-"`
	UnitValue          UnitValue        `yaml:"unitValue,omitempty" valid:"-"`
	UnitCost           UnitCost         `yaml:"unitCost" valid:"required"`
	UnitQuotaThreshold uint32           `yaml:"unitQuotaThreshold" valid:"-"`
}

type CCUnitType struct {
	Value tarrif_asn.Enumerated `yaml:"value" valid:"required"`
}

type UnitCost struct {
	ValueDigits int64 `yaml:"valueDigits" valid:"required"`
	Exponent    int32 `yaml:"exponent" valid:"required"`
}

type UnitValue struct {
	ValueDigits int64 `yaml:"valueDigits" valid:"required"`
	Exponent    int32 `yaml:"exponent" valid:"required"`
}

type ChargeReasonCode struct {
	Value tarrif_asn.Enumerated `yaml:"value" valid:"required"`
}

type Tls struct {
	Pem string `yaml:"pem,omitempty" valid:"type(string),minstringlength(1),required"`
	Key string `yaml:"key,omitempty" valid:"type(string),minstringlength(1),required"`
}

func (t *Tls) validate() (bool, error) {
	result, err := govalidator.ValidateStruct(t)
	return result, err
}

func appendInvalid(err error) error {
	var errs govalidator.Errors

	es := err.(govalidator.Errors).Errors()
	for _, e := range es {
		errs = append(errs, fmt.Errorf("Invalid %w", e))
	}

	return error(errs)
}

func (c *Config) GetVersion() string {
	if c.Info != nil && c.Info.Version != "" {
		return c.Info.Version
	}
	return ""
}
