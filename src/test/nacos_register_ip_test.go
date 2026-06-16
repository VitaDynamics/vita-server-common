package test

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/VitaDynamics/vita-server-common/src/nacos"
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

func TestResolveRegisterIP_FallbackToLocalIPv4(t *testing.T) {
	t.Setenv("NACOS_REGISTER_IP", "")

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
