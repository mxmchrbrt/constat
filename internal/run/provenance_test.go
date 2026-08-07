package run

import (
	"testing"

	"github.com/mxmchrbrt/constat/internal/config"
	"github.com/mxmchrbrt/constat/internal/container"
)

// The report has to state which Postgres a dump came from and which one it
// was loaded into (taxonomy #9) — and has to stay silent, rather than emit an
// empty object, for a target that has no database at all.
func TestDatabaseProvenance(t *testing.T) {
	tests := []struct {
		name       string
		verifyWith *config.VerifyWith
		pg         *container.Postgres
		wantNil    bool
		wantDump   string
		wantServer string
		wantImage  string
	}{
		{
			name:    "no database at all records nothing",
			wantNil: true,
		},
		{
			name:       "image alone is worth recording when the container never came up",
			verifyWith: &config.VerifyWith{Image: "postgres:16-alpine", Load: "dump.sql"},
			wantImage:  "postgres:16-alpine",
		},
		{
			name:       "both versions and the image",
			verifyWith: &config.VerifyWith{Image: "postgres:16-alpine", Load: "dump.sql"},
			pg: &container.Postgres{
				Versions: container.Versions{Dump: "16.14", Server: "16.10"},
			},
			wantDump:   "16.14",
			wantServer: "16.10",
			wantImage:  "postgres:16-alpine",
		},
		{
			name:       "a dump with no readable header still records the server",
			verifyWith: &config.VerifyWith{Image: "postgres:15-alpine", Load: "dump.pgc"},
			pg: &container.Postgres{
				Versions: container.Versions{Server: "15.8"},
			},
			wantServer: "15.8",
			wantImage:  "postgres:15-alpine",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := databaseProvenance(config.Target{VerifyWith: tt.verifyWith}, tt.pg)

			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a database record, got nil")
			}
			if got.DumpVersion != tt.wantDump {
				t.Errorf("DumpVersion = %q, want %q", got.DumpVersion, tt.wantDump)
			}
			if got.ServerVersion != tt.wantServer {
				t.Errorf("ServerVersion = %q, want %q", got.ServerVersion, tt.wantServer)
			}
			if got.Image != tt.wantImage {
				t.Errorf("Image = %q, want %q", got.Image, tt.wantImage)
			}
		})
	}
}
