package test

import (
	"testing"

	"github.com/VitaDynamics/vita-server-common/src/nacos"
)

func TestResolveMaxAttempts(t *testing.T) {
	tests := []struct {
		name  string
		param nacos.GrpcCallParam
		want  int
	}{
		{
			name:  "retry disabled",
			param: nacos.GrpcCallParam{Retry: false},
			want:  1,
		},
		{
			name:  "retry enabled with default count",
			param: nacos.GrpcCallParam{Retry: true, RetryCount: 0},
			want:  2,
		},
		{
			name:  "retry enabled with explicit count",
			param: nacos.GrpcCallParam{Retry: true, RetryCount: 3},
			want:  4,
		},
		{
			name:  "retry enabled with negative count uses default",
			param: nacos.GrpcCallParam{Retry: true, RetryCount: -1},
			want:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nacos.ResolveMaxAttempts(tt.param); got != tt.want {
				t.Fatalf("nacos.ResolveMaxAttempts() = %d, want %d", got, tt.want)
			}
		})
	}
}
