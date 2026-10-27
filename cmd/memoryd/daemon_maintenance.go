package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

func runDaemonWriteGate(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	op, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	status, err := pg.ManageRecoveryWrites(op, cfg.WriteGate, cfg.ExpectedStorage, cfg.ExpectedGeneration)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(status)
}

func runDaemonRecallBaseline(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	if flag.NFlag() > 2 || flag.NArg() != 0 || cfg.Worker || cfg.Recovery || cfg.InitDB {
		return memory.ErrInvalid // a measurement never shares a run with a writing mode
	}
	return runRecallBaseline(ctx, pg, cfg.RecallBaseline, cfg.BaselineScope)
}

func runDaemonInitDB(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	op, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return pg.Init(op)
}

func runDaemonSemanticIndex(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	if cfg.SemanticIndex != "create" && cfg.SemanticIndex != "drop" || os.Getenv("MEMORY_SEMANTIC_RETRIEVAL") != "1" {
		return memory.ErrInvalid
	}
	op, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return pg.InitSemanticIndex(op, cfg.SemanticIndex == "create")
}

func runDaemonCheckIndexes(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	return pg.SearchReady(ctx)
}

func runDaemonMemoryReach(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	if cfg.MemoryReach != "apply" && cfg.MemoryReach != "revert" {
		return memory.ErrInvalid
	}
	changed, err := pg.MigrateMemoryReach(ctx, cfg.MemoryReach == "revert")
	fmt.Printf("{\"changed\":%d,\"direction\":%q}\n", changed, cfg.MemoryReach)
	return err
}

func runDaemonBackupExpiry(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	oldest, err := time.Parse(time.RFC3339, cfg.BackupExpiry)
	if err != nil {
		return memory.ErrInvalid
	}
	auth, err := configuredAuthorizer(pg, standalone)
	if err != nil {
		return err
	}
	svc := &memory.Service{Repo: pg, Auth: auth}
	op, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	advanced, err := svc.ExpirePrivacyBackups(op, os.Getenv("MEMORY_WORKER_TOKEN"), oldest)
	fmt.Printf("{\"advanced\":%d}\n", advanced) // rows past a failed one still advanced
	return err
}

func runDaemonSnapshot(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	op, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var data []byte
	var err error
	if cfg.ReceiptCauses {
		data, err = pg.RecoveryReviewSnapshot(op, cfg.InspectRestore, cfg.ExpectedGeneration)
	} else if cfg.ExpectedGeneration != "" {
		data, err = pg.RecoverySnapshotAtWriteGate(op, cfg.InspectRestore, cfg.ExpectedGeneration)
	} else if cfg.InspectRestore {
		data, err = pg.InspectRestoredRecovery(op)
	} else {
		data, err = pg.ExportRecoverySnapshot(op)
	}
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func runDaemonBlobs(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	auth, err := configuredAuthorizer(pg, standalone)
	if err != nil {
		return err
	}
	svc := &memory.Service{Repo: pg, Auth: auth, BlobDirectory: os.Getenv("MEMORY_BLOB_DIRECTORY")}
	if cfg.AdoptBlobs {
		if err = svc.AdoptRestoredBlobs(ctx, os.Getenv("MEMORY_WORKER_TOKEN")); err != nil {
			return err
		}
		fmt.Println("restored file inventory bound; publication blocked pending deletion and expiry review")
		return nil
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result, err := svc.CollectBlobGarbage(op, os.Getenv("MEMORY_WORKER_TOKEN"))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func runDaemonMaintenance(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) (bool, error) {
	mode, err := daemonMode(cfg)
	if err != nil {
		return true, err
	}
	switch mode {
	case "WriteGate":
		return true, runDaemonWriteGate(ctx, pg, cfg, standalone)
	case "TrainReview":
		return true, runTrainReviewClassifier(ctx, pg, cfg.TrainReview, cfg.BaselineScope, cfg.TrainReviewExclude)
	case "RecallBaseline":
		return true, runDaemonRecallBaseline(ctx, pg, cfg, standalone)
	case "InitDB":
		return true, runDaemonInitDB(ctx, pg, cfg, standalone)
	case "SemanticIndex":
		return true, runDaemonSemanticIndex(ctx, pg, cfg, standalone)
	case "CheckIndexes":
		return true, runDaemonCheckIndexes(ctx, pg, cfg, standalone)
	case "MemoryReach":
		return true, runDaemonMemoryReach(ctx, pg, cfg, standalone)
	case "BackupExpiry":
		return true, runDaemonBackupExpiry(ctx, pg, cfg, standalone)
	case "AuditExpiry":
		return true, runDaemonAuditExpiry(ctx, pg, cfg, standalone)
	case "ExportSnapshot", "InspectRestore":
		return true, runDaemonSnapshot(ctx, pg, cfg, standalone)
	case "CollectBlobs", "AdoptBlobs":
		return true, runDaemonBlobs(ctx, pg, cfg, standalone)
	}
	return false, nil
}
