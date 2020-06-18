package vault

import (
	"strings"
	"testing"
	"time"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/vault/helper/metricsutil"
	"github.com/hashicorp/vault/helper/namespace"
	"github.com/hashicorp/vault/sdk/logical"
)

func TestCoreMetrics_KvSecretGauge(t *testing.T) {
	core, _, root := TestCoreUnsealed(t)

	// I think I can't test with the real kv-v2 because it's
	// a plugin. But we can fake it by using "metadata" as
	// part of the secret path with a V1 backend.
	testMounts := []struct {
		Path          string
		Version       string
		ExpectedCount int
	}{
		{"secret/", "2", 0},
		{"secret1/", "1", 3},
		{"secret2/", "1", 0},
		{"secret3/", "2", 4},
		{"prefix/secret3/", "2", 0},
		{"prefix/secret4/", "2", 5},
	}
	ctx := namespace.RootContext(nil)

	// skip 0, secret/ is already mounted
	for _, tm := range testMounts[1:] {
		me := &MountEntry{
			Table:   mountTableType,
			Path:    sanitizeMountPath(tm.Path),
			Type:    "kv",
			Options: map[string]string{"version": tm.Version},
		}
		err := core.mount(ctx, me)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
	}

	secrets := []string{
		"secret1/a", // 3
		"secret1/b",
		"secret1/c/d",
		"secret3/metadata/a", // 4
		"secret3/metadata/b",
		"secret3/metadata/c/d",
		"secret3/metadata/c/e",
		"prefix/secret4/metadata/a/secret", // 5
		"prefix/secret4/metadata/a/secret2",
		"prefix/secret4/metadata/a/b/c/secret",
		"prefix/secret4/metadata/a/b/c/secret2",
		"prefix/secret4/metadata/a/b/c/d/secret3",
	}
	for _, p := range secrets {
		req := logical.TestRequest(t, logical.CreateOperation, p)
		req.Data["foo"] = "bar"
		req.ClientToken = root
		resp, err := core.HandleRequest(ctx, req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if resp != nil {
			t.Fatalf("bad: %#v", resp)
		}
	}

	values, err := core.kvSecretGaugeCollector(ctx)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(values) != len(testMounts) {
		t.Errorf("Got %v values but expected %v mounts", len(values), len(testMounts))
	}

	for _, glv := range values {
		mountPoint := ""
		for _, l := range glv.Labels {
			if l.Name == "mount_point" {
				mountPoint = l.Value
			} else if l.Name == "namespace" {
				if l.Value != "root" {
					t.Errorf("Namespace is %v, not root", l.Value)
				}
			} else {
				t.Errorf("Unexpected label %v", l.Name)
			}
		}
		if mountPoint == "" {
			t.Errorf("No mount point in labels %v", glv.Labels)
			continue
		}
		found := false
		for _, tm := range testMounts {
			if tm.Path == mountPoint {
				found = true
				if glv.Value != float32(tm.ExpectedCount) {
					t.Errorf("Mount %v reported %v, not %v",
						tm.Path, glv.Value, tm.ExpectedCount)
				}
				break
			}
		}
		if !found {
			t.Errorf("Unexpected mount point %v", mountPoint)
		}
	}
}

func TestCoreMetrics_KvSecretGaugeError(t *testing.T) {
	core := TestCore(t)

	// Replace metricSink before unsealing
	inmemSink := metrics.NewInmemSink(
		1000000*time.Hour,
		2000000*time.Hour)
	core.metricSink = metricsutil.NewClusterMetricSink("test-cluster", inmemSink)

	testCoreUnsealed(t, core)
	ctx := namespace.RootContext(nil)

	badKvMount := &kvMount{
		Namespace:  namespace.RootNamespace,
		MountPoint: "bad/path",
		Version:    "1",
		NumSecrets: 0,
	}

	core.walkKvMountSecrets(ctx, badKvMount)

	intervals := inmemSink.Data()
	// Test crossed an interval boundary, don't try to deal with it.
	if len(intervals) > 1 {
		t.Skip("Detected interval crossing.")
	}

	// Should be an error
	keyPrefix := "metrics.collection.error"
	var counter *metrics.SampledValue = nil

	for _, c := range intervals[0].Counters {
		if strings.HasPrefix(c.Name, keyPrefix) {
			counter = &c
			break
		}
	}
	if counter == nil {
		t.Fatal("No metrics.collection.error counter found.")
	}
	if counter.Count != 1 {
		t.Errorf("Counter number of samples %v is not 1.", counter.Count)
	}

}
