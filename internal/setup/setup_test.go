package setup

import (
	"strings"
	"testing"
)

func TestMemoryPresetOptionsOrderAndLargeDefault(t *testing.T) {
	opts := MemoryPresetOptions()
	if len(opts) != 4 {
		t.Fatalf("expected 4 presets, got %d", len(opts))
	}
	names := []MemoryProfileName{ProfileSmall, ProfileMedium, ProfileLarge, ProfileVeryLarge}
	for i, want := range names {
		if opts[i].Name != want {
			t.Fatalf("preset[%d] = %q, want %q", i, opts[i].Name, want)
		}
	}
	large := opts[2]
	if large.Tuning.SharedBuffersMB != 2048 || large.Tuning.EffectiveCacheSizeMB != 6144 || large.Tuning.WorkMemMB != 16 || large.Tuning.MaintenanceWorkMemMB != 512 {
		t.Fatalf("Large Library preset tuning mismatch: %+v", large.Tuning)
	}
}

func TestResolveMemoryTuningNamedPresets(t *testing.T) {
	tn, err := ResolveMemoryTuning(ProfileLarge, 0)
	if err != nil {
		t.Fatalf("ResolveMemoryTuning(large) error = %v", err)
	}
	if tn.SharedBuffersMB != 2048 {
		t.Fatalf("SharedBuffersMB = %d, want 2048", tn.SharedBuffersMB)
	}
}

func TestResolveMemoryTuningCustomBudget(t *testing.T) {
	tn, err := ResolveMemoryTuning(ProfileCustom, 8192)
	if err != nil {
		t.Fatalf("ResolveMemoryTuning(custom,8192) error = %v", err)
	}
	// The adaptive formula should land close to (but need not exactly
	// match) the hardcoded Large preset for the same budget.
	if tn.SharedBuffersMB != 2048 || tn.EffectiveCacheSizeMB != 6144 {
		t.Fatalf("adaptive tuning at 8192MB = %+v, want shared=2048 effective=6144", tn)
	}
	if tn.MaintenanceWorkMemMB != 512 {
		t.Fatalf("adaptive maintenance_work_mem at 8192MB = %d, want 512", tn.MaintenanceWorkMemMB)
	}
}

func TestResolveMemoryTuningCustomBudgetTooSmall(t *testing.T) {
	if _, err := ResolveMemoryTuning(ProfileCustom, 100); err == nil {
		t.Fatal("expected error for a too-small custom budget")
	}
}

func TestResolveMemoryTuningUnknownProfile(t *testing.T) {
	if _, err := ResolveMemoryTuning("nonsense", 4096); err == nil {
		t.Fatal("expected error for unknown profile name")
	}
}

func TestAdaptiveMemoryTuningCapsMaintenanceWorkMemAtOneGB(t *testing.T) {
	tn := adaptiveMemoryTuning(64 * 1024) // 64GB budget
	if tn.MaintenanceWorkMemMB != 1024 {
		t.Fatalf("MaintenanceWorkMemMB = %d, want capped at 1024", tn.MaintenanceWorkMemMB)
	}
	if tn.AutovacuumWorkMemMB != 256 {
		t.Fatalf("AutovacuumWorkMemMB = %d, want capped at 256 even though maintenance_work_mem is larger", tn.AutovacuumWorkMemMB)
	}
	if tn.WorkMemMB > 64 {
		t.Fatalf("WorkMemMB = %d, want capped at 64", tn.WorkMemMB)
	}
}

func TestResolveIOTuningStorageProfiles(t *testing.T) {
	ssd, err := ResolveIOTuning(StorageSSD)
	if err != nil {
		t.Fatalf("ResolveIOTuning(ssd) error = %v", err)
	}
	hdd, err := ResolveIOTuning(StorageHDD)
	if err != nil {
		t.Fatalf("ResolveIOTuning(hdd) error = %v", err)
	}
	if ssd.RandomPageCost >= hdd.RandomPageCost {
		t.Fatalf("expected SSD random_page_cost (%v) to be lower than HDD's (%v)", ssd.RandomPageCost, hdd.RandomPageCost)
	}
	if ssd.EffectiveIOConcurrency <= hdd.EffectiveIOConcurrency {
		t.Fatalf("expected SSD effective_io_concurrency (%v) to be higher than HDD's (%v)", ssd.EffectiveIOConcurrency, hdd.EffectiveIOConcurrency)
	}
	if _, err := ResolveIOTuning("tape"); err == nil {
		t.Fatal("expected error for unknown storage profile")
	}
}

func baseOptions() ComposeOptions {
	tn, _ := ResolveMemoryTuning(ProfileLarge, 0)
	return ComposeOptions{
		DatabaseName: "javbeacon",
		DatabaseUser: "javbeacon",
		Password:     "s3cret-pass",
		DataPath:     "./postgres-data",
		Topology:     TopologyHost,
		Port:         5432,
		Storage:      StorageSSD,
		Memory:       tn,
	}
}

func TestComposeOptionsValidateRejectsMissingFields(t *testing.T) {
	o := baseOptions()
	o.DatabaseName = ""
	if err := o.Validate(); err == nil {
		t.Fatal("expected error for missing database name")
	}
	o = baseOptions()
	o.Password = ""
	if err := o.Validate(); err == nil {
		t.Fatal("expected error for missing password")
	}
	o = baseOptions()
	o.Port = 70000
	if err := o.Validate(); err == nil {
		t.Fatal("expected error for out-of-range port")
	}
}

func TestComposeOptionsValidateRejectsPublicLANBind(t *testing.T) {
	o := baseOptions()
	o.Topology = TopologyLAN
	o.BindAddress = "0.0.0.0"
	if err := o.Validate(); err == nil {
		t.Fatal("expected error for 0.0.0.0 LAN bind address")
	}
	o.BindAddress = ""
	if err := o.Validate(); err == nil {
		t.Fatal("expected error for empty LAN bind address")
	}
	o.BindAddress = "192.168.1.50"
	if err := o.Validate(); err != nil {
		t.Fatalf("unexpected error for a specific LAN address: %v", err)
	}
}

func TestGenerateComposeSameNetworkOmitsPorts(t *testing.T) {
	o := baseOptions()
	o.Topology = TopologySameNetwork
	fixed := DefaultFixedTuning()
	io, _ := ResolveIOTuning(o.Storage)
	yaml, err := GenerateCompose(o, fixed, io)
	if err != nil {
		t.Fatalf("GenerateCompose error = %v", err)
	}
	if strings.Contains(yaml, "ports:") {
		t.Fatal("same-network topology must not publish a ports block")
	}
	if !strings.Contains(yaml, "postgres:5432") {
		t.Fatal("same-network topology should mention the internal service hostname")
	}
	if !strings.Contains(yaml, "image: postgres:18") {
		t.Fatal("compose should pin postgres:18")
	}
	if !strings.Contains(yaml, "/var/lib/postgresql") || strings.Contains(yaml, "/var/lib/postgresql/data") {
		t.Fatalf("compose must mount the PostgreSQL 18 volume target, not the pre-18 /data path")
	}
}

func TestGenerateComposeHostTopologyPublishesLoopback(t *testing.T) {
	o := baseOptions()
	o.Topology = TopologyHost
	fixed := DefaultFixedTuning()
	io, _ := ResolveIOTuning(o.Storage)
	yaml, err := GenerateCompose(o, fixed, io)
	if err != nil {
		t.Fatalf("GenerateCompose error = %v", err)
	}
	if !strings.Contains(yaml, "ports:") {
		t.Fatal("host topology should publish a ports block")
	}
	if !strings.Contains(yaml, "${POSTGRES_BIND_ADDRESS:-127.0.0.1}") {
		t.Fatal("host topology should default the bind address to loopback")
	}
}

func TestGenerateComposeLANTopologyWarns(t *testing.T) {
	o := baseOptions()
	o.Topology = TopologyLAN
	o.BindAddress = "192.168.1.50"
	fixed := DefaultFixedTuning()
	io, _ := ResolveIOTuning(o.Storage)
	yaml, err := GenerateCompose(o, fixed, io)
	if err != nil {
		t.Fatalf("GenerateCompose error = %v", err)
	}
	if !strings.Contains(strings.ToUpper(yaml), "WARNING") {
		t.Fatal("LAN topology compose should include a security warning comment")
	}
}

func TestGenerateComposeRejectsInvalidOptions(t *testing.T) {
	o := baseOptions()
	o.Topology = TopologyLAN
	o.BindAddress = "0.0.0.0"
	fixed := DefaultFixedTuning()
	io, _ := ResolveIOTuning(o.Storage)
	if _, err := GenerateCompose(o, fixed, io); err == nil {
		t.Fatal("expected GenerateCompose to reject invalid options")
	}
}

func TestGenerateEnvContainsAllExpectedKeys(t *testing.T) {
	o := baseOptions()
	fixed := DefaultFixedTuning()
	io, _ := ResolveIOTuning(o.Storage)
	env, err := GenerateEnv(o, fixed, io)
	if err != nil {
		t.Fatalf("GenerateEnv error = %v", err)
	}
	for _, key := range []string{
		"POSTGRES_DB=javbeacon",
		"POSTGRES_USER=javbeacon",
		"POSTGRES_PASSWORD=s3cret-pass",
		"POSTGRES_DATA_PATH=./postgres-data",
		"POSTGRES_BIND_ADDRESS=127.0.0.1",
		"POSTGRES_PORT=5432",
		"POSTGRES_MAX_CONNECTIONS=100",
		"POSTGRES_SHARED_BUFFERS=2048MB",
		"POSTGRES_EFFECTIVE_CACHE_SIZE=6144MB",
		"POSTGRES_WORK_MEM=16MB",
		"POSTGRES_MAINTENANCE_WORK_MEM=512MB",
		"POSTGRES_RANDOM_PAGE_COST=1.1",
		"POSTGRES_EFFECTIVE_IO_CONCURRENCY=200",
		"POSTGRES_WAL_COMPRESSION=on",
	} {
		if !strings.Contains(env, key) {
			t.Fatalf("generated .env missing %q\n--- env ---\n%s", key, env)
		}
	}
}

func TestGeneratePasswordIsRandomAndURLSafe(t *testing.T) {
	a, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword error = %v", err)
	}
	b, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword error = %v", err)
	}
	if a == b {
		t.Fatal("two generated passwords must not be equal")
	}
	if len(a) < 24 {
		t.Fatalf("generated password too short: %d chars", len(a))
	}
	if strings.ContainsAny(a, "\"'\n\t $&") {
		t.Fatalf("generated password contains characters that need quoting in .env/YAML: %q", a)
	}
}

func TestDefaultApplicationPoolMatchesLargeLibraryTarget(t *testing.T) {
	p := DefaultApplicationPool()
	if p.MaxOpenConns < 20 || p.MaxOpenConns > 30 {
		t.Fatalf("MaxOpenConns = %d, want in [20,30]", p.MaxOpenConns)
	}
	if p.MaxIdleConns < 5 || p.MaxIdleConns > 10 {
		t.Fatalf("MaxIdleConns = %d, want in [5,10]", p.MaxIdleConns)
	}
}
