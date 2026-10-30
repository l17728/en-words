package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
)

// WithAtomicWrite is outer-only. It owns safe COMMIT cleanup and obtains the
// SQLite writer before any configuration or card snapshot is read.
func WithAtomicWrite(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return WithAtomicWriteObserved(ctx, db, fn, nil)
}

// WithAtomicWriteObserved reports transaction phases after connection release.
// The observer is best-effort and cannot alter the write result.
func WithAtomicWriteObserved(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error, observe func(AtomicConnectionTiming)) error {
	return atomicConnectionObserved(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE config_globals SET key=key WHERE 1=0").Error; err != nil {
			return err
		}
		return fn(tx)
	}, observe)
}
func (r *SchedulerRepo) CardStudentIDs(ctx context.Context) ([]int64, error) {
	var ids []int64
	err := r.db.WithContext(ctx).Model(&MemoryCard{}).Distinct("student_id").Order("student_id").Pluck("student_id", &ids).Error
	return ids, err
}
func (r *SchedulerRepo) ClampLadderSteps(ctx context.Context, sid int64, max int) (int64, error) {
	res := r.db.WithContext(ctx).Model(&MemoryCard{}).Where("student_id=? AND ladder_step>?", sid, max).UpdateColumn("ladder_step", max)
	return res.RowsAffected, res.Error
}

// ConfigReservedStudents is a read-only conservative boundary, not a Pilot
// authorization receipt. No reservation is created/deleted and no Pilot factory runs.
func ConfigReservedStudents(tx *gorm.DB) (map[int64]bool, error) {
	var objects []struct {
		Name   string
		Type   string
		Origin string
	}
	if err := tx.Raw("SELECT name,type,'main' AS origin FROM main.sqlite_master WHERE name = ? COLLATE NOCASE UNION ALL SELECT name,type,'temp' AS origin FROM temp.sqlite_master WHERE name = ? COLLATE NOCASE", "classroom_scope_reservations", "classroom_scope_reservations").Scan(&objects).Error; err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	if len(objects) == 0 {
		return out, nil
	}
	if len(objects) != 1 || objects[0].Origin != "main" || objects[0].Name != "classroom_scope_reservations" || objects[0].Type != "table" {
		return nil, fmt.Errorf("atomic_configuration_unavailable")
	}
	var malformed int64
	if err := tx.Table("classroom_scope_reservations").Where("student_id IS NULL OR mode IS NULL OR schema_version IS NULL OR reserved_at IS NULL").Count(&malformed).Error; err != nil || malformed != 0 {
		return nil, fmt.Errorf("atomic_configuration_unavailable")
	}
	var rows []struct {
		StudentID     int64
		Mode          string
		SchemaVersion int64
		ReservedAt    int64
	}
	if err := tx.Table("classroom_scope_reservations").Select("student_id,mode,schema_version,reserved_at").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("atomic_configuration_unavailable")
	}
	for _, r := range rows {
		if r.StudentID <= 0 {
			return nil, fmt.Errorf("atomic_configuration_unavailable")
		}
		if r.Mode != "legacy" || r.SchemaVersion != 2 || r.ReservedAt < 0 {
			out[r.StudentID] = true
		}
	}
	return out, nil
}
func (p *ConfigPersister) LoadSnapshot() (g map[string]json.RawMessage, o map[int64]map[string]json.RawMessage, err error) {
	read := func(db *gorm.DB) error {
		r := NewConfigPersister(db)
		var e error
		g, e = r.LoadGlobals()
		if e != nil {
			return e
		}
		o, e = r.LoadOverrides()
		return e
	}
	if _, outer := p.db.Statement.ConnPool.(*sql.DB); outer {
		err = p.db.Transaction(read)
	} else {
		err = read(p.db)
	}
	return
}
