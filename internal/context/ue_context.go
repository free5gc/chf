package context

import "github.com/free5gc/CDRUtil/cdrType"

type ChfUe struct {
	Supi string
	CDR  map[string][]cdrType.CHFRecord
}
