package cluster_test

import (
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/influxdb/influxdb/cluster"
)

func TestConfig_Parse(t *testing.T) {
	// Parse configuration.
	var c cluster.Config
	if _, err := toml.Decode(`
bind-address = ":8080"
shard-writer-timeout = "10s"
`, &c); err != nil {
		t.Fatal(err)
	}

	// Validate configuration.
	if c.BindAddress != ":8080" {
		t.Fatalf("unexpected bind address: %s", c.BindAddress)
	} else if time.Duration(c.ShardWriterTimeout) != 10*time.Second {
		t.Fatalf("unexpected bind address: %s", c.ShardWriterTimeout)
	}
}
