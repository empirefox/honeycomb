package hctox

import (
	"testing"
)

func TestToxID(t *testing.T) {
	toxid, err := ToxID("CF43FFE81487EA74A519C568E5D2CD79611D3661919617B9D8E542F4ECAB8977", 3368701159)
	if err != nil {
		t.Errorf("Failed to get toxid: %v", err)
		return
	}
	if toxid != "9AA0FF6C243F90947035FA4AA45353A705B4F9681299AC61A295E3C32911EB63C8CA4CE7E9C1" {
		t.Errorf("Wrong toxid: %s", toxid)
	}
}
