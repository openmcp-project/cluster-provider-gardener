package app

import "testing"

func TestShootPrometheusObservabilityEnabledFromEnv(t *testing.T) {
	for _, tt := range []struct {
		name    string
		value   string
		want    bool
		wantErr bool
	}{
		{name: "unset", want: false},
		{name: "enabled", value: "true", want: true},
		{name: "disabled", value: "false", want: false},
		{name: "invalid", value: "sometimes", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(shootPrometheusObservabilityEnabledEnv, tt.value)
			got, err := shootPrometheusObservabilityEnabledFromEnv()
			if (err != nil) != tt.wantErr {
				t.Fatalf("shootPrometheusObservabilityEnabledFromEnv() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("shootPrometheusObservabilityEnabledFromEnv() = %t, want %t", got, tt.want)
			}
		})
	}
}
