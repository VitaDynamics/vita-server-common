package test

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/VitaDynamics/vita-server-common/src/utils/nacos"
)

func TestResolveRegisterIP_FromEnv(t *testing.T) {
	t.Setenv("NACOS_REGISTER_IP", "10.20.30.40")

	ip, err := nacos.ResolveRegisterIP()
	if err != nil {
		t.Fatalf("ResolveRegisterIP() error = %v", err)
	}
	if ip != "10.20.30.40" {
		t.Fatalf("ResolveRegisterIP() = %q, want %q", ip, "10.20.30.40")
	}
}

func TestResolveRegisterIP_InvalidEnv(t *testing.T) {
	t.Setenv("NACOS_REGISTER_IP", "not-an-ip")

	_, err := nacos.ResolveRegisterIP()
	if err == nil {
		t.Fatal("ResolveRegisterIP() expected error for invalid NACOS_REGISTER_IP")
	}
	if !strings.Contains(err.Error(), "invalid NACOS_REGISTER_IP") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveRegisterIP_FromCIDR(t *testing.T) {
	localIP := firstTestLocalIPv4(t)
	t.Setenv("NACOS_REGISTER_IP", "")
	t.Setenv("NACOS_REGISTER_CIDR", localIP+"/32")
	t.Setenv("NACOS_REGISTER_INTERFACE", "")

	ip, err := nacos.ResolveRegisterIP()
	if err != nil {
		t.Fatalf("ResolveRegisterIP() error = %v", err)
	}
	if ip != localIP {
		t.Fatalf("ResolveRegisterIP() = %q, want %q", ip, localIP)
	}
}

func TestResolveRegisterIP_InvalidCIDR(t *testing.T) {
	t.Setenv("NACOS_REGISTER_IP", "")
	t.Setenv("NACOS_REGISTER_CIDR", "not-a-cidr")

	_, err := nacos.ResolveRegisterIP()
	if err == nil {
		t.Fatal("ResolveRegisterIP() expected error for invalid NACOS_REGISTER_CIDR")
	}
	if !strings.Contains(err.Error(), "invalid NACOS_REGISTER_CIDR") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveRegisterIP_MissingInterface(t *testing.T) {
	t.Setenv("NACOS_REGISTER_IP", "")
	t.Setenv("NACOS_REGISTER_CIDR", "")
	t.Setenv("NACOS_REGISTER_INTERFACE", "nacos-test-missing-interface")

	_, err := nacos.ResolveRegisterIP()
	if err == nil {
		t.Fatal("ResolveRegisterIP() expected error for missing NACOS_REGISTER_INTERFACE")
	}
	if !strings.Contains(err.Error(), "NACOS_REGISTER_INTERFACE") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveRegisterIP_FallbackToLocalIPv4(t *testing.T) {
	t.Setenv("NACOS_REGISTER_IP", "")
	t.Setenv("NACOS_REGISTER_CIDR", "")
	t.Setenv("NACOS_REGISTER_INTERFACE", "")

	ip, err := nacos.ResolveRegisterIP()
	t.Log("Resolved IP:", ip)
	if err != nil {
		t.Fatalf("ResolveRegisterIP() error = %v", err)
	}

	parsed := net.ParseIP(ip)
	t.Log("Resolved parsed:", parsed)
	if parsed == nil {
		t.Fatalf("ResolveRegisterIP() returned invalid IP: %q", ip)
	}
	if parsed.To4() == nil {
		t.Fatalf("ResolveRegisterIP() = %q, want IPv4", ip)
	}
	if parsed.IsLoopback() {
		t.Fatalf("ResolveRegisterIP() = %q, should not be loopback", ip)
	}
}

func TestResolveRegisterIP_EnvTakesPrecedence(t *testing.T) {
	t.Setenv("NACOS_REGISTER_IP", "192.168.1.100")
	t.Setenv("NACOS_REGISTER_CIDR", "not-a-cidr")
	t.Setenv("NACOS_REGISTER_INTERFACE", "nacos-test-missing-interface")

	ip, err := nacos.ResolveRegisterIP()
	t.Log("Resolved IP:", ip)
	if err != nil {
		t.Fatalf("ResolveRegisterIP() error = %v", err)
	}
	if ip != "192.168.1.100" {
		t.Fatalf("ResolveRegisterIP() = %q, want env value", ip)
	}

	// Ensure fallback path is not used when env is set.
	_ = os.Unsetenv("NACOS_REGISTER_IP")
}

func firstTestLocalIPv4(t *testing.T) string {
	t.Helper()

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("InterfaceAddrs() error = %v", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP == nil {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return ip.String()
	}
	t.Skip("no non-loopback ipv4 address available")
	return ""
}
