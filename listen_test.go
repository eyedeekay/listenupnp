package lup

import (
	"testing"
)

func TestGetInternalIPAddress(t *testing.T) {
	var upl UpListener
	ip, err := upl.GetInternalIPAddress()
	if err != nil {
		t.Error(err)
	}
	t.Log(ip)
}
