package store

import (
	"encoding/json"
	"enwords/internal/memory"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RuntimeRecord stores versioned application state without importing api.
type RuntimeRecord struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

type ConfigPersister struct{ db *gorm.DB }

func NewConfigPersister(db *gorm.DB) *ConfigPersister { return &ConfigPersister{db} }
func (p *ConfigPersister) LoadGlobals() (map[string]json.RawMessage, error) {
	var rows []ConfigGlobal
	if err := p.db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, r := range rows {
		out[r.Key] = json.RawMessage(r.Value)
	}
	return out, nil
}
func (p *ConfigPersister) LoadOverrides() (map[int64]map[string]json.RawMessage, error) {
	var rows []ConfigOverride
	if err := p.db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[int64]map[string]json.RawMessage{}
	for _, r := range rows {
		if out[r.StudentID] == nil {
			out[r.StudentID] = map[string]json.RawMessage{}
		}
		out[r.StudentID][r.Key] = json.RawMessage(r.Value)
	}
	return out, nil
}
func (p *ConfigPersister) SaveGlobal(key string, val any) error {
	b, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return p.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&ConfigGlobal{Key: key, Value: string(b)}).Error
}
func (p *ConfigPersister) SaveOverride(sid int64, key string, val any) error {
	b, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return p.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "student_id"}, {Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&ConfigOverride{StudentID: sid, Key: key, Value: string(b)}).Error
}
func (p *ConfigPersister) DeleteOverride(sid int64, key string) error {
	return p.db.Where("student_id=? AND key=?", sid, key).Delete(&ConfigOverride{}).Error
}

// Agent memories use a distinct table to preserve all domain fields, including revision links.
type DurableMemory memory.Memory
type MemoryRepo struct{ db *gorm.DB }

func NewMemoryRepo(db *gorm.DB) *MemoryRepo { return &MemoryRepo{db} }
func (p *MemoryRepo) Append(m *memory.Memory) error {
	row := DurableMemory(*m)
	if err := p.db.Create(&row).Error; err != nil {
		return err
	}
	m.ID = row.ID
	return nil
}
func (p *MemoryRepo) Get(id int64) (*memory.Memory, error) {
	var r DurableMemory
	if err := p.db.First(&r, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, memory.ErrNotFound
		}
		return nil, err
	}
	m := memory.Memory(r)
	return &m, nil
}
func (p *MemoryRepo) Update(m *memory.Memory) error {
	r := DurableMemory(*m)
	return p.db.Save(&r).Error
}
func (p *MemoryRepo) Delete(id int64) error { return p.db.Delete(&DurableMemory{}, id).Error }
func (p *MemoryRepo) List(owner memory.OwnerRole, sid, bid int64) ([]*memory.Memory, error) {
	var rows []DurableMemory
	if err := p.db.Where("owner_role=? AND student_id=? AND book_id=?", string(owner), sid, bid).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := []*memory.Memory{}
	for _, r := range rows {
		m := memory.Memory(r)
		out = append(out, &m)
	}
	return out, nil
}
