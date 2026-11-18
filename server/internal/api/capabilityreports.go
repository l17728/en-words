package api

import (
	"encoding/json"
	"enwords/internal/classroom"
	"enwords/internal/store"
	"fmt"
	"gorm.io/gorm"
)

func capabilityReportTx(tx *gorm.DB, id int64) (*classroom.Result, error) {
	var row store.RuntimeRecord
	if err := tx.Where("key=?", fmt.Sprintf("report/%d", id)).First(&row).Error; err != nil {
		return nil, err
	}
	var result classroom.Result
	if err := json.Unmarshal([]byte(row.Value), &result); err != nil {
		return nil, err
	}
	return &result, nil
}
func (a *App) capabilityReport(id int64) (*classroom.Result, error) {
	if a.DB != nil {
		return capabilityReportTx(a.DB, id)
	}
	result, ok := a.Report(id)
	if !ok {
		return nil, fmt.Errorf("lesson report unavailable")
	}
	return result, nil
}
