package main

import "testing"

func TestBenchmarkImportSourcePriorityAndOriginConflict(t *testing.T) {
	store := newModelBenchmarkStore(t.TempDir())
	record := completedBenchmarkForReport("origin-benchmark")
	record.OriginDeviceID = "device-a"
	record.OriginDeviceName = "테스트 PC A"
	record.OriginBenchmarkID = "origin-benchmark"

	first, err := store.importRecordsFromSource([]ModelBenchmark{record}, benchmarkSourceSync, nil)
	if err != nil || len(first.Imported) != 1 {
		t.Fatalf("sync import = %#v, %v", first, err)
	}
	stored, err := store.Open(first.Imported[0].ID)
	if err != nil || stored.Source != benchmarkSourceSync {
		t.Fatalf("stored sync record = %#v, %v", stored, err)
	}

	// The same payload imported through a report must not be duplicated. Its
	// stronger report provenance replaces the weaker sync provenance instead.
	report, err := store.importRecordsFromSource([]ModelBenchmark{record}, benchmarkSourceReport, nil)
	if err != nil || report.DuplicateCount != 1 || report.UpgradedCount != 1 {
		t.Fatalf("report upgrade = %#v, %v", report, err)
	}
	stored, err = store.Open(stored.ID)
	if err != nil || stored.Source != benchmarkSourceReport || !stored.Imported {
		t.Fatalf("upgraded record = %#v, %v", stored, err)
	}

	conflicting := record
	conflicting.ID = "another-local-id"
	conflicting.Model = "changed-model"
	conflicting.UpdatedAt = "2026-09-10T01:02:04Z"
	conflict, err := store.importRecordsFromSource([]ModelBenchmark{conflicting}, benchmarkSourceSync, nil)
	if err != nil || conflict.ConflictCount != 1 || len(conflict.Imported) != 0 {
		t.Fatalf("origin conflict = %#v, %v", conflict, err)
	}
}
