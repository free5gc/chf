package producer

import (
	"fmt"
	"reflect"

	// "encoding/hex"

	"github.com/free5gc/CDRUtil/asn"
	"github.com/free5gc/CDRUtil/cdrFile"
	"github.com/free5gc/CDRUtil/cdrType"
)

func getCdrByte(supi string) (out []byte) {
	cr := cdrType.ChargingRecord{
		SubscriberIdentifier: &cdrType.SubscriptionID{SubscriptionIDData: asn.UTF8String(supi)},
		ListOfMultipleUnitUsage: []cdrType.MultipleUnitUsage{
			{
				RatingGroup: cdrType.RatingGroupId{1},
				UsedUnitContainers: []cdrType.UsedUnitContainer{
					{
						DataTotalVolume:    &cdrType.DataVolumeOctets{6912},
						DataVolumeUplink:   &cdrType.DataVolumeOctets{1234},
						DataVolumeDownlink: &cdrType.DataVolumeOctets{6789},
					},
					{
						DataTotalVolume:    &cdrType.DataVolumeOctets{3},
						DataVolumeUplink:   &cdrType.DataVolumeOctets{1},
						DataVolumeDownlink: &cdrType.DataVolumeOctets{2},
					},
				},
			},
		},
	}

	out, _ = asn.BerMarshalWithParams(cr, "")
	return out
}

func getcdr(supi string) (cdr cdrFile.CDR, cdrLen uint16) {
	cdrByte := getCdrByte(supi)

	cdr = cdrFile.CDR{
		Hdr: cdrFile.CdrHeader{
			CdrLength:                  uint16(len(cdrByte)),
			ReleaseIdentifier:          cdrFile.Rel6,
			VersionIdentifier:          3,
			DataRecordFormat:           cdrFile.UnalignedPackedEncodingRules,
			TsNumber:                   cdrFile.TS32253,
			ReleaseIdentifierExtension: 4,
		},
		CdrByte: cdrByte,
	}

	cdrLen = uint16(len(cdrByte)) + 5

	return
}

func getCdrFile(supi string) (cf cdrFile.CDRFile) {
	cdr, cdrLen := getcdr(supi)

	cfhdr := cdrFile.CdrFileHeader{
		FileLength:                            66 + uint32(cdrLen),
		HeaderLength:                          66,
		HighReleaseIdentifier:                 4,
		HighVersionIdentifier:                 5,
		LowReleaseIdentifier:                  5,
		LowVersionIdentifier:                  6,
		FileOpeningTimestamp:                  cdrFile.CdrHdrTimeStamp{1, 2, 11, 56, 1, 7, 30},
		TimestampWhenLastCdrWasAppendedToFIle: cdrFile.CdrHdrTimeStamp{4, 3, 2, 1, 0, 4, 0},
		NumberOfCdrsInFile:                    1,
		FileSequenceNumber:                    65,
		FileClosureTriggerReason:              2,
		IpAddressOfNodeThatGeneratedFile:      [20]byte{0xc, 0xd, 0xc, 0xd, 0xc, 0xd, 0xc, 0xd, 0xc, 0xd, 0xc, 0xd, 0xc, 0xd, 0xc, 0xd, 0xc, 0xd, 0xc, 0xd},
		LostCdrIndicator:                      4,
		LengthOfCdrRouteingFilter:             5,
		CDRRouteingFilter:                     []byte("gfdss"),
		LengthOfPrivateExtension:              7,
		PrivateExtension:                      []byte("abcdefg"), // vendor specific
		HighReleaseIdentifierExtension:        1,
		LowReleaseIdentifierExtension:         2,
	}

	cf = cdrFile.CDRFile{
		Hdr:     cfhdr,
		CdrList: []cdrFile.CDR{cdr},
	}
	return
}

func gen_file() {
	supi := "imsi-208930000000003"
	cf := getCdrFile(supi)

	fileName := "/home/uduck/" + supi + ".cdr"

	cf.Encoding(fileName)

	newCdrFile := cdrFile.CDRFile{}
	newCdrFile.Decoding(fileName)

	recvByte := newCdrFile.CdrList[0].CdrByte

	val := reflect.New(reflect.TypeOf(&cdrType.ChargingRecord{}).Elem()).Interface()

	asn.UnmarshalWithParams(recvByte, val, "")

	chargingRecord := *(val.(*cdrType.ChargingRecord))

	var total_cnt int64 = 0
	var ul_cnt int64 = 0
	var dl_cnt int64 = 0

	for _, multipleUnitUsage := range chargingRecord.ListOfMultipleUnitUsage {
		for _, usedUnitContainer := range multipleUnitUsage.UsedUnitContainers {
			total_cnt += usedUnitContainer.DataTotalVolume.Value
			ul_cnt += usedUnitContainer.DataVolumeUplink.Value
			dl_cnt += usedUnitContainer.DataVolumeDownlink.Value
		}
	}
	fmt.Println(total_cnt, ul_cnt, dl_cnt)
	fmt.Println((*chargingRecord.SubscriberIdentifier).SubscriptionIDData)
}

func main() {
	supi := "imsi-208930000000003"
	cf := getCdrFile(supi)

	fileName := "/tmp/" + supi + ".cdr"

	cf.Encoding(fileName)

	newCdrFile := cdrFile.CDRFile{}
	newCdrFile.Decoding(fileName)

	recvByte := newCdrFile.CdrList[0].CdrByte

	val := reflect.New(reflect.TypeOf(&cdrType.ChargingRecord{}).Elem()).Interface()

	asn.UnmarshalWithParams(recvByte, val, "")

	chargingRecord := *(val.(*cdrType.ChargingRecord))

	var total_cnt int64 = 0
	var ul_cnt int64 = 0
	var dl_cnt int64 = 0

	for _, multipleUnitUsage := range chargingRecord.ListOfMultipleUnitUsage {
		for _, usedUnitContainer := range multipleUnitUsage.UsedUnitContainers {
			total_cnt += usedUnitContainer.DataTotalVolume.Value
			ul_cnt += usedUnitContainer.DataVolumeUplink.Value
			dl_cnt += usedUnitContainer.DataVolumeDownlink.Value
		}
	}
	fmt.Println(total_cnt, ul_cnt, dl_cnt)
	fmt.Println((*chargingRecord.SubscriberIdentifier).SubscriptionIDData)

}
