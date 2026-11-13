package drive

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Linux CD-ROM ioctls, from <linux/cdrom.h>.
const (
	cdromDriveStatus = 0x5326
	cdromEject       = 0x5309

	cdsNoInfo        = 0
	cdsNoDisc        = 1
	cdsTrayOpen      = 2
	cdsDriveNotReady = 3
	cdsDiscOK        = 4
)

// Status asks the kernel about the drive's tray. O_NONBLOCK lets the device
// open without a disc in it.
func Status(device string) (State, error) {
	fd, err := unix.Open(device, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return Unknown, fmt.Errorf("opening %s: %w", device, err)
	}
	defer unix.Close(fd)
	r, err := unix.IoctlRetInt(fd, cdromDriveStatus)
	if err != nil {
		return Unknown, fmt.Errorf("reading the status of %s: %w", device, err)
	}
	switch r {
	case cdsNoDisc:
		return NoDisc, nil
	case cdsTrayOpen:
		return TrayOpen, nil
	case cdsDriveNotReady:
		return NotReady, nil
	case cdsDiscOK:
		return DiscOK, nil
	}
	return Unknown, nil
}

// Eject opens the drive's tray.
func Eject(device string) error {
	fd, err := unix.Open(device, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", device, err)
	}
	defer unix.Close(fd)
	if err := unix.IoctlSetInt(fd, cdromEject, 0); err != nil {
		return fmt.Errorf("ejecting %s: %w", device, err)
	}
	return nil
}

var _ = cdsNoInfo // listed for completeness: "no information" reads as Unknown
