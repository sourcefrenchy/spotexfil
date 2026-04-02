package c2

import (
	"encoding/json"
	"net"
	"os"
	"os/user"
	"runtime"
)

// SysinfoModule gathers system information.
type SysinfoModule struct{}

func (m *SysinfoModule) Name() string { return "sysinfo" }

func (m *SysinfoModule) Execute(args map[string]interface{}) (string, string) {
	hostname, _ := os.Hostname()
	cwd, _ := os.Getwd()

	info := map[string]interface{}{
		"os":       runtime.GOOS + "/" + runtime.GOARCH,
		"hostname": hostname,
		"username": getUsername(),
		"ips":      getIPs(),
		"pid":      os.Getpid(),
		"cwd":      cwd,
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return "error", err.Error()
	}
	return "ok", string(data)
}

func getUsername() string {
	u, err := user.Current()
	if err != nil {
		return "unknown"
	}
	return u.Username
}

func getIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() {
				s := ip.String()
				ips = append(ips, s)
			}
		}
	}
	return ips
}
