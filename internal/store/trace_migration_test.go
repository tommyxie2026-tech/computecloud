package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func seedHistoricalV7(t *testing.T, variant string) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, schema := range []string{ServerSchema, serverV2, serverV3, serverV4, serverV5, serverV6} {
		if _, err = db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	schema := serverV7
	if variant == "trace" {
		schema = serverV14
	}
	if _, err = db.Exec(schema + "PRAGMA user_version=7;"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"INSERT INTO tasks(id,owner,project,idem,hash,spec,state,created,updated,deadline) VALUES('t','o','p','i','h','{}','SUCCEEDED',1,2,100)",
		"INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released) VALUES('a','t','w','e',1,'token',99,1)",
		"INSERT INTO gateway_requests(id,owner,project,route,model,endpoint,state,started) VALUES('r','o','p','route','model','/v1/responses','COMPLETE',1)",
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if variant == "trace" {
		if _, err = db.Exec("INSERT INTO traces VALUES('trc_old','o','p','conversation',1); UPDATE gateway_requests SET trace_id='trc_old'; INSERT INTO attempt_metrics VALUES('a','{}',1)"); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err = db.Exec("UPDATE attempts SET last_renewed=42; INSERT INTO event_dedup VALUES('a',1,'old-hash')"); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestV13UpgradesBothHistoricalV7Schemas(t *testing.T) {
	for _, variant := range []string{"main", "trace"} {
		t.Run(variant, func(t *testing.T) {
			dir := seedHistoricalV7(t, variant)
			backup, err := OpenForBackup(dir)
			if err != nil {
				t.Fatal(err)
			}
			var version int
			if err = backup.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
				t.Fatalf("backup version=%d err=%v", version, err)
			}
			backup.Close()
			db, err := Open(dir, ServerSchema)
			if err != nil {
				t.Fatal(err)
			}
			var renewed int
			if err = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 14 {
				t.Fatalf("version=%d err=%v", version, err)
			}
			if err = db.SQL.QueryRow("SELECT last_renewed FROM attempts WHERE id='a'").Scan(&renewed); err != nil {
				t.Fatal(err)
			}
			if variant == "trace" {
				var trace, payload string
				if err = db.SQL.QueryRow("SELECT trace_id FROM gateway_requests WHERE id='r'").Scan(&trace); err != nil || trace != "trc_old" {
					t.Fatalf("trace=%q err=%v", trace, err)
				}
				if err = db.SQL.QueryRow("SELECT payload FROM attempt_metrics WHERE attempt_id='a'").Scan(&payload); err != nil || payload != "{}" {
					t.Fatalf("metrics=%q err=%v", payload, err)
				}
				if renewed != 99 {
					t.Fatalf("liveness not backfilled: %d", renewed)
				}
			} else {
				var hash string
				if err = db.SQL.QueryRow("SELECT hash FROM event_dedup WHERE attempt='a' AND worker_seq=1").Scan(&hash); err != nil || hash != "old-hash" || renewed != 42 {
					t.Fatalf("liveness/dedup changed: %d %s %v", renewed, hash, err)
				}
				if _, err = db.SQL.Exec("INSERT INTO traces VALUES('trc_new','o','p','job',2)"); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			db, err = Open(dir, ServerSchema)
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
		})
	}
}

func TestV13DrainAndPartialSchemaRollback(t *testing.T) {
	for _, variant := range []string{"main", "trace"} {
		for _, failure := range []string{"attempt", "gateway", "partial"} {
			t.Run(variant+"/"+failure, func(t *testing.T) {
				dir := seedHistoricalV7(t, variant)
				raw, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				statement := "UPDATE attempts SET released=0"
				if failure == "gateway" {
					statement = "UPDATE gateway_requests SET state='STARTED'"
				}
				if failure == "partial" {
					statement = "DROP TABLE event_dedup"
					if variant == "trace" {
						statement = "DROP TABLE attempt_metrics"
					}
				}
				if _, err = raw.Exec(statement); err != nil {
					t.Fatal(err)
				}
				raw.Close()
				if db, err := Open(dir, ServerSchema); err == nil {
					db.Close()
					t.Fatal("unsafe upgrade accepted")
				}
				raw, err = sql.Open("sqlite", filepath.Join(dir, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				var version, n int
				if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
					t.Fatalf("failed upgrade version=%d err=%v", version, err)
				}
				table := "traces"
				if variant == "trace" {
					table = "event_dedup"
				}
				if err = raw.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", table).Scan(&n); err != nil || n != 0 {
					t.Fatalf("partial migration: %s=%d err=%v", table, n, err)
				}
			})
		}
	}
}

// Cover main v8-v11 and the already-rebased experimental v8, which has the
// same version number as ACP but no control_operations table.
func TestV13UpgradesMainAndExperimentalV8(t *testing.T) {
	for _, variant := range []string{"trace8", "main8", "main9", "main10", "main11"} {
		t.Run(variant, func(t *testing.T) {
			base := "main"
			if variant == "trace8" {
				base = "trace"
			}
			dir := seedHistoricalV7(t, base)
			raw, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			stmts := []string{serverV7, "PRAGMA user_version=8"}
			if base == "main" {
				stmts = []string{serverV8, "PRAGMA user_version=8"}
				if variant != "main8" {
					stmts = append(stmts, serverV9, "PRAGMA user_version=9")
				}
				if variant == "main10" || variant == "main11" {
					stmts = append(stmts, serverV10, "PRAGMA user_version=10")
				}
				if variant == "main11" {
					stmts = append(stmts, serverV11, "PRAGMA user_version=11")
				}
				stmts = append(stmts, "INSERT INTO control_operations(principal_id,operation_id,operation_type,resource_type,resource_id,request_hash,state,receipt_json,created,updated) VALUES('o','op','cancel','job','j','h','COMPLETED','{}',1,1)")
			}
			for _, stmt := range stmts {
				if _, err = raw.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			raw.Close()
			db, err := Open(dir, ServerSchema)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var version int
			if err = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 14 {
				t.Fatalf("version=%d err=%v", version, err)
			}
			for _, query := range []string{"SELECT count(*) FROM control_operations", "SELECT count(*) FROM replan_history", "SELECT count(*) FROM traces"} {
				var n int
				if err = db.SQL.QueryRow(query).Scan(&n); err != nil {
					t.Fatal(err)
				}
			}
			var value string
			if base == "trace" {
				err = db.SQL.QueryRow("SELECT trace_id FROM gateway_requests WHERE id='r'").Scan(&value)
				if err != nil || value != "trc_old" {
					t.Fatalf("lost trace: %s %v", value, err)
				}
			} else {
				err = db.SQL.QueryRow("SELECT state FROM control_operations WHERE operation_id='op'").Scan(&value)
				if err != nil || value != "COMPLETED" {
					t.Fatalf("lost control: %s %v", value, err)
				}
			}
		})
	}
}

func TestV13UpgradesBothV12Variants(t *testing.T) {
	for _, variant := range []string{"main", "trace"} {
		for _, broken := range []bool{false, true} {
			name := variant
			if broken {
				name += "/rollback"
			}
			t.Run(name, func(t *testing.T) {
				dir := seedHistoricalV7(t, "main")
				raw, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				schema := serverV12
				if variant == "trace" {
					schema = serverV14
				}
				for _, stmt := range []string{serverV8, serverV9, serverV10, serverV11, schema, "PRAGMA user_version=12"} {
					if _, err = raw.Exec(stmt); err != nil {
						t.Fatal(err)
					}
				}
				if variant == "trace" {
					_, err = raw.Exec("INSERT INTO traces VALUES('trc_v12','o','p','job',1); UPDATE gateway_requests SET trace_id='trc_v12'; INSERT INTO attempt_metrics VALUES('a','{}',1)")
				} else {
					_, err = raw.Exec(`INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism) VALUES('j','o','p','i','h','h','{}','single','SUCCEEDED',1,1,100,1);
    INSERT INTO approval_requests(approval_id,request_version,job_id,task_id,attempt_id,generation,session_id,tool,action,risk_class,request_hash,requested_at,state) VALUES('approval',1,'j','t','a',1,'s','shell','run','LOW','hash',1,'PENDING');`)
				}
				if err != nil {
					t.Fatal(err)
				}
				if broken {
					stmt := "DROP TABLE approval_requests"
					if variant == "trace" {
						stmt = "DROP TABLE attempt_metrics"
					}
					if _, err = raw.Exec(stmt); err != nil {
						t.Fatal(err)
					}
				}
				raw.Close()
				db, err := Open(dir, ServerSchema)
				if broken {
					if err == nil {
						db.Close()
						t.Fatal("partial v12 accepted")
					}
					raw, err = sql.Open("sqlite", filepath.Join(dir, "state.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer raw.Close()
					var version, n int
					if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 12 {
						t.Fatalf("rollback version=%d err=%v", version, err)
					}
					table := "traces"
					if variant == "trace" {
						table = "approval_requests"
					}
					if err = raw.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", table).Scan(&n); err != nil || n != 0 {
						t.Fatalf("partial migration: %s %d %v", table, n, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var version int
				if err = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 14 {
					t.Fatalf("version=%d err=%v", version, err)
				}
				var value string
				if variant == "trace" {
					if err = db.SQL.QueryRow("SELECT trace_id FROM gateway_requests WHERE id='r'").Scan(&value); err != nil || value != "trc_v12" {
						t.Fatalf("lost trace %s %v", value, err)
					}
					if err = db.SQL.QueryRow("SELECT payload FROM attempt_metrics WHERE attempt_id='a'").Scan(&value); err != nil || value != "{}" {
						t.Fatalf("lost metrics %s %v", value, err)
					}
				} else {
					if err = db.SQL.QueryRow("SELECT state FROM approval_requests WHERE approval_id='approval'").Scan(&value); err != nil || value != "PENDING" {
						t.Fatalf("lost approval %s %v", value, err)
					}
				}
				for _, table := range []string{"traces", "attempt_metrics", "approval_requests", "replan_history"} {
					var n int
					if err = db.SQL.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
