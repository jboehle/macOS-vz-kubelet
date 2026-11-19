package netutil

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/virtual-kubelet/virtual-kubelet/log"
)

// RetrieveVMIP returns the IP address of the VM with macAddr, and where it was found ("arp" or "dhcp-leases").
// It polls the ARP table and, when that has no entry for the address, bootpd's leases file, once a second until the
// context ends. On macOS 27 `arp -an` can come back without the VM's entry (see openai/tart#1359) although bootpd
// has leased it an address, so the leases file is the fallback. It logs what each source saw, so a failure can be
// diagnosed from the kubelet's log.
func RetrieveVMIP(ctx context.Context, macAddr string) (ip string, source string, err error) {
	logger := log.G(ctx).WithField("mac", macAddr)
	start := time.Now()
	var arpLines, bridgeLines int
	var bridgeSample []string
	var leaseErr error
	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			logger.WithField("seconds", int(time.Since(start).Seconds())).
				WithField("arpLines", arpLines).
				WithField("arpBridgeLines", bridgeLines).
				WithField("arpBridgeSample", strings.Join(bridgeSample, " | ")).
				WithField("leasesError", errString(leaseErr)).
				Warn("VM IP lookup timed out: the address was in neither the ARP table nor the DHCP leases")
			return "", "", ctx.Err()
		default:
		}

		out, arpErr := exec.CommandContext(ctx, "arp", "-an").Output()
		if arpErr == nil {
			arpLines, bridgeLines, bridgeSample = 0, 0, nil
			want := strings.ToLower(macAddr)
			for _, line := range strings.Split(string(out), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				arpLines++
				if strings.Contains(line, " on bridge") {
					bridgeLines++
					if len(bridgeSample) < 3 {
						bridgeSample = append(bridgeSample, strings.TrimSpace(line))
					}
				}
				if strings.Contains(strings.ToLower(line), want) {
					if fields := strings.Fields(line); len(fields) > 1 {
						return strings.Trim(fields[1], "()"), "arp", nil
					}
				}
			}
		} else if ctx.Err() == nil {
			logger.WithError(arpErr).Warn("arp -an failed")
		}

		leaseIP, ok, err := LookupDHCPLease(DHCPLeasesPath, macAddr)
		leaseErr = err
		if ok {
			return leaseIP, "dhcp-leases", nil
		}

		if attempt%10 == 1 {
			logger.WithField("attempt", attempt).
				WithField("arpLines", arpLines).
				WithField("arpBridgeLines", bridgeLines).
				WithField("leasesError", errString(leaseErr)).
				Info("waiting for the VM's IP address")
		}
		time.Sleep(time.Second)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
