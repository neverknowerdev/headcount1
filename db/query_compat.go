package db

import "time"

// ptrTime remains package-local for existing database tests. Repository code
// owns its equivalent helper alongside Run persistence.
func ptrTime(value time.Time) *time.Time { return &value }
