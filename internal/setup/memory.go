// Package setup implements DB Phase 1A of TODO-DATABASE.md: the first-run
// / Settings "Database Setup Wizard" that generates a ready-to-run
// PostgreSQL Docker Compose stack (compose.yaml + .env) tailored to the
// user's chosen memory budget, storage medium and network topology.
//
// This package only produces text (YAML/dotenv) and configuration values -
// it never opens a network connection and never talks to PostgreSQL itself.
// Actually connecting to PostgreSQL (driver, pooling, Test/Validate
// Connection) is DB Phases 3-4 and deliberately out of scope here.
package setup

import (
	"errors"
	"math"
)

// StorageProfile selects the I/O planner defaults used for the generated
// PostgreSQL tuning flags. SSD/NVMe is the recommended default; HDD uses
// conservative, non-SSD planner assumptions instead of reusing SSD values.
type StorageProfile string

const (
	StorageSSD StorageProfile = "ssd"
	StorageHDD StorageProfile = "hdd"
)

// MemoryProfileName identifies one of the named presets from
// TODO-DATABASE.md's "Adaptive memory sizing" table, or "custom" for a
// user-supplied memory budget.
type MemoryProfileName string

const (
	ProfileSmall     MemoryProfileName = "small"
	ProfileMedium    MemoryProfileName = "medium"
	ProfileLarge     MemoryProfileName = "large" // default per DB Phase 1A
	ProfileVeryLarge MemoryProfileName = "very_large"
	ProfileCustom    MemoryProfileName = "custom"
)

// MemoryTuning holds the memory-related PostgreSQL settings that scale with
// the selected memory budget. All *MB fields are in megabytes.
type MemoryTuning struct {
	BudgetMB             int
	SharedBuffersMB      int
	EffectiveCacheSizeMB int
	WorkMemMB            int
	MaintenanceWorkMemMB int
	AutovacuumWorkMemMB  int
}

// namedPreset is a hardcoded row from the TODO-DATABASE.md preset table.
// Presets are hardcoded (rather than derived purely from the adaptive
// formula) so the documented defaults - especially the Large Library
// default - match the spec exactly rather than approximately.
type namedPreset struct {
	label  string
	tuning MemoryTuning
}

var namedPresets = map[MemoryProfileName]namedPreset{
	ProfileSmall: {
		label:  "Small (~2 GB)",
		tuning: MemoryTuning{BudgetMB: 2048, SharedBuffersMB: 512, EffectiveCacheSizeMB: 1536, WorkMemMB: 8, MaintenanceWorkMemMB: 128, AutovacuumWorkMemMB: 128},
	},
	ProfileMedium: {
		label:  "Medium (~4 GB)",
		tuning: MemoryTuning{BudgetMB: 4096, SharedBuffersMB: 1024, EffectiveCacheSizeMB: 3072, WorkMemMB: 12, MaintenanceWorkMemMB: 256, AutovacuumWorkMemMB: 256},
	},
	ProfileLarge: {
		label:  "Large Library / Cross-reference heavy (~8 GB) - recommended",
		tuning: MemoryTuning{BudgetMB: 8192, SharedBuffersMB: 2048, EffectiveCacheSizeMB: 6144, WorkMemMB: 16, MaintenanceWorkMemMB: 512, AutovacuumWorkMemMB: 256},
	},
	ProfileVeryLarge: {
		label:  "Very Large (~16 GB)",
		tuning: MemoryTuning{BudgetMB: 16384, SharedBuffersMB: 4096, EffectiveCacheSizeMB: 12288, WorkMemMB: 24, MaintenanceWorkMemMB: 1024, AutovacuumWorkMemMB: 256},
	},
}

// MemoryPresetOption is a wire-friendly description of one preset, for the
// wizard UI to render as a set of choices.
type MemoryPresetOption struct {
	Name   MemoryProfileName `json:"name"`
	Label  string            `json:"label"`
	Tuning MemoryTuning      `json:"tuning"`
}

// MemoryPresetOptions returns the presets in table order (Small, Medium,
// Large, Very Large) for the wizard UI. Large Library is the recommended
// default per DB Phase 1A.
func MemoryPresetOptions() []MemoryPresetOption {
	order := []MemoryProfileName{ProfileSmall, ProfileMedium, ProfileLarge, ProfileVeryLarge}
	out := make([]MemoryPresetOption, 0, len(order))
	for _, name := range order {
		p := namedPresets[name]
		out = append(out, MemoryPresetOption{Name: name, Label: p.label, Tuning: p.tuning})
	}
	return out
}

// ResolveMemoryTuning returns the memory tuning for a named preset, or - for
// ProfileCustom - computes an adaptive tuning from budgetMB using the
// starting-point percentage rules from TODO-DATABASE.md's "Adaptive memory
// sizing" section:
//
//   - shared_buffers          ~25%   of the budget
//   - effective_cache_size    ~75%   of the budget
//   - maintenance_work_mem    ~6.25% of the budget, capped at 1 GB
//   - autovacuum_work_mem     bounded to maintenance_work_mem, capped at 256 MB
//     (multiple autovacuum workers may use it concurrently)
//   - work_mem                calculated conservatively (sub-linear in the
//     budget, since many sorts/hashes/sessions may allocate it at once)
//
// These are documented as "starting points, not immutable constants" - the
// wizard surfaces the computed values and allows advanced overrides before
// generating the final Compose/.env files.
func ResolveMemoryTuning(name MemoryProfileName, customBudgetMB int) (MemoryTuning, error) {
	if p, ok := namedPresets[name]; ok {
		return p.tuning, nil
	}
	if name != ProfileCustom {
		return MemoryTuning{}, errors.New("unknown memory profile: " + string(name))
	}
	if customBudgetMB < 256 {
		return MemoryTuning{}, errors.New("custom memory budget must be at least 256 MB")
	}
	return adaptiveMemoryTuning(customBudgetMB), nil
}

func adaptiveMemoryTuning(budgetMB int) MemoryTuning {
	shared := int(math.Round(float64(budgetMB) * 0.25))
	effective := int(math.Round(float64(budgetMB) * 0.75))
	maintenance := int(math.Round(float64(budgetMB) * 0.0625))
	if maintenance > 1024 {
		maintenance = 1024
	}
	if maintenance < 32 {
		maintenance = 32
	}
	autovacuum := maintenance
	if autovacuum > 256 {
		autovacuum = 256
	}
	// Conservative, sub-linear work_mem: scales with sqrt(budget) rather
	// than linearly, since many concurrent sort/hash operations can each
	// allocate up to work_mem at once. Clamped to a sane range.
	work := int(math.Round(math.Sqrt(float64(budgetMB)) * 0.18))
	if work < 4 {
		work = 4
	}
	if work > 64 {
		work = 64
	}
	return MemoryTuning{
		BudgetMB:             budgetMB,
		SharedBuffersMB:      shared,
		EffectiveCacheSizeMB: effective,
		WorkMemMB:            work,
		MaintenanceWorkMemMB: maintenance,
		AutovacuumWorkMemMB:  autovacuum,
	}
}

// IOTuning holds storage-medium-dependent planner settings.
type IOTuning struct {
	RandomPageCost         float64
	EffectiveIOConcurrency int
}

// ResolveIOTuning returns the planner I/O settings for the given storage
// profile. SSD/NVMe (the recommended default) reuses the low
// random_page_cost / high concurrency values from the Large Library
// template; HDD deliberately does not reuse those SSD assumptions and
// instead uses PostgreSQL's traditional conservative rotational-disk
// defaults.
func ResolveIOTuning(storage StorageProfile) (IOTuning, error) {
	switch storage {
	case StorageSSD, "":
		return IOTuning{RandomPageCost: 1.1, EffectiveIOConcurrency: 200}, nil
	case StorageHDD:
		return IOTuning{RandomPageCost: 4.0, EffectiveIOConcurrency: 2}, nil
	default:
		return IOTuning{}, errors.New("unknown storage profile: " + string(storage))
	}
}

// FixedTuning holds the PostgreSQL settings that TODO-DATABASE.md's Large
// Library template fixes regardless of memory budget or storage profile
// (connection limits, WAL/checkpoint behavior, autovacuum cadence and
// planner statistics target).
type FixedTuning struct {
	MaxConnections               int
	DefaultStatisticsTarget      int
	WALCompression               string
	MinWALSizeGB                 int
	MaxWALSizeGB                 int
	CheckpointCompletionTarget   float64
	AutovacuumMaxWorkers         int
	AutovacuumNaptimeSeconds     int
	AutovacuumVacuumScaleFactor  float64
	AutovacuumAnalyzeScaleFactor float64
}

// DefaultFixedTuning returns the Large Library template's fixed settings.
func DefaultFixedTuning() FixedTuning {
	return FixedTuning{
		MaxConnections:               100,
		DefaultStatisticsTarget:      200,
		WALCompression:               "on",
		MinWALSizeGB:                 2,
		MaxWALSizeGB:                 8,
		CheckpointCompletionTarget:   0.9,
		AutovacuumMaxWorkers:         5,
		AutovacuumNaptimeSeconds:     30,
		AutovacuumVacuumScaleFactor:  0.05,
		AutovacuumAnalyzeScaleFactor: 0.02,
	}
}
