package config

import "testing"

func validTestSRS(size int) []byte {
	if size < 4 {
		size = 4
	}
	data := make([]byte, size)
	copy(data, []byte{'S', 'R', 'S', 1})
	return data
}

func TestIsSingBoxRuleSetData_AllowsSmallValidSRS(t *testing.T) {
	if !IsSingBoxRuleSetData(validTestSRS(159)) {
		t.Fatal("small SRS binary should be accepted")
	}
}

func TestIsSingBoxRuleSetData_RejectsHTML(t *testing.T) {
	if IsSingBoxRuleSetData([]byte("<!DOCTYPE html><html></html>")) {
		t.Fatal("HTML response must not be accepted as SRS")
	}
}
