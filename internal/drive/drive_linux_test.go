package drive

import (
	"strings"
	"testing"
)

// These run on Linux only (CI). They can't put a disc in a drive, but they do
// run the real ioctl calls, so a wrong request number or a call that panics
// is caught here rather than on the first real disc. The full check, a real
// disc in a real drive, is in the deployment steps (docs/DESIGN.md, section
// 14).

func TestStatusOnSomethingThatIsNotADrive(t *testing.T) {
	// /dev/null opens fine but is not a CD-ROM: the ioctl must fail cleanly.
	s, err := Status("/dev/null")
	if err == nil {
		t.Fatalf("Status(/dev/null) = %v with no error, want an error", s)
	}
	if !strings.Contains(err.Error(), "/dev/null") {
		t.Errorf("error %q doesn't name the device", err)
	}
}

func TestStatusOnAMissingDevice(t *testing.T) {
	if _, err := Status("/dev/no-such-drive"); err == nil || !strings.Contains(err.Error(), "opening") {
		t.Errorf("err = %v, want an opening error", err)
	}
}

func TestEjectOnSomethingThatIsNotADrive(t *testing.T) {
	if err := Eject("/dev/null"); err == nil {
		t.Error("ejecting /dev/null succeeded")
	}
}
