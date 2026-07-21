package db

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"
)

type recordingAdditiveMigrator struct {
	tableExists   bool
	columns       map[string]bool
	indexes       map[string]bool
	createdTables int
	addedColumns  []string
	createdIndex  []string
	failColumn    string
}

func (migrator *recordingAdditiveMigrator) HasTable(interface{}) bool {
	return migrator.tableExists
}

func (migrator *recordingAdditiveMigrator) CreateTable(...interface{}) error {
	migrator.createdTables++
	return nil
}

func (migrator *recordingAdditiveMigrator) HasColumn(_ interface{}, field string) bool {
	return migrator.columns[field]
}

func (migrator *recordingAdditiveMigrator) AddColumn(_ interface{}, field string) error {
	migrator.addedColumns = append(migrator.addedColumns, field)
	if field == migrator.failColumn {
		return errors.New("migration failed")
	}
	return nil
}

func (migrator *recordingAdditiveMigrator) HasIndex(_ interface{}, name string) bool {
	return migrator.indexes[name]
}

func (migrator *recordingAdditiveMigrator) CreateIndex(_ interface{}, name string) error {
	migrator.createdIndex = append(migrator.createdIndex, name)
	return nil
}

func testMigrationPlan(count int) []Migration {
	plan := make([]Migration, 0, count)
	for version := 1; version <= count; version++ {
		plan = append(plan, Migration{Version: version, Name: "test", Apply: func(*gorm.DB) error { return nil }})
	}
	return plan
}

func TestPendingMigrationsSupportsFirstRepeatAndLegacyUpgrade(t *testing.T) {
	plan := testMigrationPlan(4)
	tests := []struct {
		name    string
		applied []int
		want    []int
	}{
		{name: "first run", want: []int{1, 2, 3, 4}},
		{name: "repeat run", applied: []int{1, 2, 3, 4}, want: []int{}},
		{name: "upgrade schema v3", applied: []int{1, 2, 3}, want: []int{4}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pending, err := pendingMigrations(plan, test.applied)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int, 0, len(pending))
			for _, migration := range pending {
				got = append(got, migration.Version)
			}
			if len(got) != len(test.want) {
				t.Fatalf("pending versions=%v want=%v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("pending versions=%v want=%v", got, test.want)
				}
			}
		})
	}
}

func TestMigrationPlanIncludesMessageRecallV5(t *testing.T) {
	plan := migrationPlan()
	if len(plan) != 5 || plan[4].Version != 5 || plan[4].Name != "message recall state" || plan[4].Apply == nil {
		t.Fatalf("unexpected migration plan tail: %+v", plan)
	}
	pending, err := pendingMigrations(plan, []int{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Version != 5 {
		t.Fatalf("schema v4 upgrade pending=%+v, want only v5", pending)
	}
}

func TestPendingMigrationsRejectsLedgerAndPlanGaps(t *testing.T) {
	if _, err := pendingMigrations(testMigrationPlan(4), []int{1, 3}); err == nil {
		t.Fatal("migration ledger gap was accepted")
	}
	plan := testMigrationPlan(3)
	plan[1].Version = 3
	if _, err := pendingMigrations(plan, nil); err == nil {
		t.Fatal("migration plan gap was accepted")
	}
}

func TestLegacyV3SnapshotLedgerIsNormalizedBeforeV4Upgrade(t *testing.T) {
	missing, normalized := legacyMigrationBackfill([]int{3})
	wantMissing := []int{1, 2}
	wantNormalized := []int{1, 2, 3}
	assertVersions(t, "missing", missing, wantMissing)
	assertVersions(t, "normalized", normalized, wantNormalized)

	pending, err := pendingMigrations(testMigrationPlan(4), normalized)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int, 0, len(pending))
	for _, migration := range pending {
		got = append(got, migration.Version)
	}
	assertVersions(t, "pending", got, []int{4})
}

func TestNormalizeLegacyV3LedgerPersistsMissingVersionsAtomically(t *testing.T) {
	database, mock := newInboxDatabase(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "schema_migrations"`).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(1))
	mock.ExpectQuery(`INSERT INTO "schema_migrations"`).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(2))
	mock.ExpectCommit()

	normalized, err := normalizeLegacyMigrationLedger(database, []int{3})
	if err != nil {
		t.Fatal(err)
	}
	assertVersions(t, "normalized", normalized, []int{1, 2, 3})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("legacy ledger backfill was not one complete transaction: %v", err)
	}
}

func TestNewMigrationLedgerGapIsNotNormalized(t *testing.T) {
	missing, normalized := legacyMigrationBackfill([]int{1, 4})
	if len(missing) != 0 {
		t.Fatalf("new migration gap was treated as a legacy snapshot: %v", missing)
	}
	if _, err := pendingMigrations(testMigrationPlan(4), normalized); err == nil {
		t.Fatal("new migration ledger gap was accepted")
	}
}

func assertVersions(t *testing.T, name string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s versions=%v want=%v", name, got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("%s versions=%v want=%v", name, got, want)
		}
	}
}

func TestNonPostgresMigrationLockSerializesConcurrentRunners(t *testing.T) {
	var active atomic.Int32
	var overlap atomic.Bool
	start := make(chan struct{})
	var runners sync.WaitGroup
	for index := 0; index < 2; index++ {
		runners.Add(1)
		go func() {
			defer runners.Done()
			<-start
			nonPostgresMigrationLock.Lock()
			if active.Add(1) != 1 {
				overlap.Store(true)
			}
			time.Sleep(10 * time.Millisecond)
			active.Add(-1)
			nonPostgresMigrationLock.Unlock()
		}()
	}
	close(start)
	runners.Wait()
	if overlap.Load() {
		t.Fatal("concurrent migration runners entered the protected section")
	}
}

func TestApplyAdditiveModelCreatesMissingTableWithoutColumnInspection(t *testing.T) {
	migrator := &recordingAdditiveMigrator{}
	if err := applyAdditiveModel(migrator, &PushMessageDelivery{}, []string{"message_id"}, []string{"idx_delivery"}); err != nil {
		t.Fatal(err)
	}
	if migrator.createdTables != 1 {
		t.Fatalf("created tables=%d want=1", migrator.createdTables)
	}
	if len(migrator.addedColumns) != 0 || len(migrator.createdIndex) != 0 {
		t.Fatalf("missing table was also altered: columns=%v indexes=%v", migrator.addedColumns, migrator.createdIndex)
	}
}

func TestApplyAdditiveModelOnlyAddsMissingColumnsAndIndexes(t *testing.T) {
	migrator := &recordingAdditiveMigrator{
		tableExists: true,
		columns:     map[string]bool{"message_id": true},
		indexes:     map[string]bool{"idx_existing": true},
	}
	err := applyAdditiveModel(migrator, &PushMessageDelivery{},
		[]string{"message_id", "status", "claim_token"},
		[]string{"idx_existing", "idx_push_delivery_retry"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(migrator.addedColumns, ","); got != "status,claim_token" {
		t.Fatalf("added columns=%q", got)
	}
	if got := strings.Join(migrator.createdIndex, ","); got != "idx_push_delivery_retry" {
		t.Fatalf("created indexes=%q", got)
	}
}

func TestApplyAdditiveModelStopsAtFirstColumnFailure(t *testing.T) {
	migrator := &recordingAdditiveMigrator{
		tableExists: true,
		columns:     map[string]bool{},
		indexes:     map[string]bool{},
		failColumn:  "status",
	}
	err := applyAdditiveModel(migrator, &PushMessageDelivery{},
		[]string{"message_id", "status", "claim_token"},
		[]string{"idx_push_delivery_retry"},
	)
	if err == nil {
		t.Fatal("column failure was ignored")
	}
	if got := strings.Join(migrator.addedColumns, ","); got != "message_id,status" {
		t.Fatalf("migration continued after failure: columns=%q", got)
	}
	if len(migrator.createdIndex) != 0 {
		t.Fatalf("indexes created after column failure: %v", migrator.createdIndex)
	}
}

func TestMigrationSessionDropsInheritedLedgerTable(t *testing.T) {
	database, mock := newInboxDatabase(t)
	dirty := database.Table("schema_migrations")
	mock.ExpectExec(`CREATE UNIQUE INDEX IF NOT EXISTS "idx_ab_experiments_experiment_key" ON "ab_experiments"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := migrationSession(dirty).Migrator().CreateIndex(&ABExperiment{}, "idx_ab_experiments_experiment_key"); err != nil {
		t.Fatalf("clean migration session retained schema_migrations table: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
