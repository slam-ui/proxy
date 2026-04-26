package config

import (
	"io"
	"os"
)

// IsSingBoxRuleSetData validates the binary sing-box rule-set header.
// Real .srs files can be very small, so size thresholds are not reliable.
func IsSingBoxRuleSetData(data []byte) bool {
	return len(data) >= 4 && data[0] == 'S' && data[1] == 'R' && data[2] == 'S'
}

// IsSingBoxRuleSetFile returns true when path looks like a sing-box binary rule-set.
func IsSingBoxRuleSetFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	header := make([]byte, 4)
	n, err := io.ReadFull(f, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		return false
	}
	return IsSingBoxRuleSetData(header[:n])
}
